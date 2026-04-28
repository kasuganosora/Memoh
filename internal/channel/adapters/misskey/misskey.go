package misskey

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/memohai/memoh/internal/channel"
	"github.com/memohai/memoh/internal/channel/common"
	"github.com/memohai/memoh/internal/textutil"
)

const (
	misskeyMaxNoteLength        = 3000
	misskeyReconnectDelayMin    = 5 * time.Second // initial reconnect delay (exponential backoff)
	misskeyReconnectDelayMax    = 5 * time.Minute // maximum reconnect delay
	misskeyPingInterval         = 30 * time.Second
	misskeyReadTimeout          = 90 * time.Second // 3× ping interval; detects half-open connections
	misskeyWriteTimeout         = 10 * time.Second
	misskeyReadBufferSize       = 1 << 16
	misskeyWriteBufferSize      = 1 << 16
	misskeyTimelineMinTextLen   = 5                // minimum text length for timeline notes
	misskeyDedupTTL             = 2 * time.Minute  // TTL for timeline note dedup cache
	misskeyDedupCleanupInterval = 30 * time.Second // cleanup interval for dedup cache
)

// MisskeyAdapter implements the channel.Adapter interfaces for Misskey.
type MisskeyAdapter struct {
	logger   *slog.Logger
	mu       sync.RWMutex
	me       map[string]*meResponse // keyed by config ID
	mentions map[string][]string    // noteID -> mention handles to reuse on reply
}

// NewMisskeyAdapter creates a MisskeyAdapter with the given logger.
func NewMisskeyAdapter(log *slog.Logger) *MisskeyAdapter {
	if log == nil {
		log = slog.Default()
	}
	return &MisskeyAdapter{
		logger:   log.With(slog.String("adapter", "misskey")),
		me:       make(map[string]*meResponse),
		mentions: make(map[string][]string),
	}
}

// Type returns the Misskey channel type.
func (*MisskeyAdapter) Type() channel.ChannelType {
	return Type
}

// Descriptor returns the Misskey channel metadata.
func (*MisskeyAdapter) Descriptor() channel.Descriptor {
	return channel.Descriptor{
		Type:        Type,
		DisplayName: "Misskey",
		Capabilities: channel.ChannelCapabilities{
			Text:           true,
			Markdown:       true,
			Reply:          true,
			Reactions:      true,
			Attachments:    true,
			Media:          true,
			Streaming:      false,
			BlockStreaming: true,
			Edit:           false,
		},
		OutboundPolicy: channel.OutboundPolicy{
			TextChunkLimit: misskeyMaxNoteLength,
			ChunkerMode:    channel.ChunkerModeMarkdown,
		},
		ConfigSchema: channel.ConfigSchema{
			Version: 1,
			Fields: map[string]channel.FieldSchema{
				"instanceURL": {
					Type:        channel.FieldString,
					Required:    true,
					Title:       "Instance URL",
					Description: "Misskey instance URL (e.g. https://misskey.io)",
					Example:     "https://misskey.io",
				},
				"accessToken": {
					Type:     channel.FieldSecret,
					Required: true,
					Title:    "Access Token",
				},
			},
		},
		UserConfigSchema: channel.ConfigSchema{
			Version: 1,
			Fields: map[string]channel.FieldSchema{
				"username": {Type: channel.FieldString},
				"user_id":  {Type: channel.FieldString},
			},
		},
		TargetSpec: channel.TargetSpec{
			Format: "user_id | @username",
			Hints: []channel.TargetHint{
				{Label: "User ID", Example: "9abcdef123456789"},
				{Label: "Username", Example: "@alice"},
			},
		},
	}
}

// --- ConfigNormalizer ---

// NormalizeConfig validates and normalizes a Misskey channel configuration map.
func (*MisskeyAdapter) NormalizeConfig(raw map[string]any) (map[string]any, error) {
	return normalizeConfig(raw)
}

// NormalizeUserConfig validates and normalizes a Misskey user-binding configuration map.
func (*MisskeyAdapter) NormalizeUserConfig(raw map[string]any) (map[string]any, error) {
	return normalizeUserConfig(raw)
}

// --- TargetResolver ---

// NormalizeTarget normalizes a Misskey delivery target string.
func (*MisskeyAdapter) NormalizeTarget(raw string) string {
	return normalizeTarget(raw)
}

// ResolveTarget derives a delivery target from a Misskey user-binding configuration.
func (*MisskeyAdapter) ResolveTarget(userConfig map[string]any) (string, error) {
	return resolveTarget(userConfig)
}

// --- BindingMatcher ---

// MatchBinding reports whether a Misskey user binding matches the given criteria.
func (*MisskeyAdapter) MatchBinding(config map[string]any, criteria channel.BindingCriteria) bool {
	return matchBinding(config, criteria)
}

// BuildUserConfig constructs a Misskey user-binding config from an Identity.
func (*MisskeyAdapter) BuildUserConfig(identity channel.Identity) map[string]any {
	return buildUserConfig(identity)
}

// --- SelfDiscoverer ---

// DiscoverSelf retrieves the bot's own identity from the Misskey platform.
func (*MisskeyAdapter) DiscoverSelf(ctx context.Context, credentials map[string]any) (map[string]any, string, error) {
	cfg, err := parseConfig(credentials)
	if err != nil {
		return nil, "", err
	}
	me, err := getMe(ctx, cfg)
	if err != nil {
		return nil, "", fmt.Errorf("misskey discover self: %w", err)
	}
	identity := map[string]any{
		"user_id":  me.ID,
		"username": me.Username,
	}
	if me.Name != "" {
		identity["name"] = me.Name
	}
	if me.AvatarURL != "" {
		identity["avatar_url"] = me.AvatarURL
	}
	return identity, me.ID, nil
}

// --- Receiver ---

// Connect starts a WebSocket streaming connection to receive Misskey mentions.
func (a *MisskeyAdapter) Connect(ctx context.Context, cfg channel.ChannelConfig, handler channel.InboundHandler) (channel.Connection, error) {
	if a.logger != nil {
		a.logger.Info("start", slog.String("config_id", cfg.ID))
	}
	mkCfg, err := parseConfig(cfg.Credentials)
	if err != nil {
		return nil, err
	}
	channel.SetIMErrorSecrets("misskey:"+cfg.ID, mkCfg.AccessToken)

	// Fetch self info for mention detection.
	me, err := getMe(ctx, mkCfg)
	if err != nil {
		return nil, fmt.Errorf("misskey get self: %w", err)
	}
	a.mu.Lock()
	a.me[cfg.ID] = me
	a.mu.Unlock()

	connCtx, cancel := context.WithCancel(ctx)
	go a.runStreamLoop(connCtx, cfg, mkCfg, me, handler)

	stop := func(_ context.Context) error {
		if a.logger != nil {
			a.logger.Info("stop", slog.String("config_id", cfg.ID))
		}
		cancel()
		return nil
	}
	return channel.NewConnection(cfg, stop), nil
}

func (a *MisskeyAdapter) runStreamLoop(ctx context.Context, cfg channel.ChannelConfig, mkCfg Config, me *meResponse, handler channel.InboundHandler) {
	delay := misskeyReconnectDelayMin
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if err := a.runStream(ctx, cfg, mkCfg, me, handler); err != nil {
			if a.logger != nil {
				a.logger.Warn("stream disconnected", slog.String("config_id", cfg.ID), slog.Any("error", err),
					slog.Duration("reconnect_delay", delay))
			}
		} else {
			// Clean disconnect (context cancelled) — no backoff needed.
			delay = misskeyReconnectDelayMin
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		// Exponential backoff: double the delay up to the maximum.
		delay *= 2
		if delay > misskeyReconnectDelayMax {
			delay = misskeyReconnectDelayMax
		}
	}
}

func (a *MisskeyAdapter) runStream(ctx context.Context, cfg channel.ChannelConfig, mkCfg Config, me *meResponse, handler channel.InboundHandler) error {
	dialer := websocket.Dialer{
		ReadBufferSize:  misskeyReadBufferSize,
		WriteBufferSize: misskeyWriteBufferSize,
	}
	conn, resp, err := dialer.DialContext(ctx, mkCfg.streamURL(), nil) //nolint:bodyclose // resp.Body is closed below
	if err != nil {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		return fmt.Errorf("misskey ws dial: %w", err)
	}
	defer func() { _ = conn.Close() }()

	if a.logger != nil {
		a.logger.Info("stream connected", slog.String("config_id", cfg.ID))
	}

	// Subscribe to main channel to receive mentions.
	connectMsg := map[string]any{
		"type": "connect",
		"body": map[string]any{
			"channel": "main",
			"id":      "memoh-main",
		},
	}
	if err := conn.WriteJSON(connectMsg); err != nil {
		return fmt.Errorf("misskey ws connect main: %w", err)
	}

	// Subscribe to timeline channels based on routing config.
	tlCfg := parseTimelineConfig(cfg.Routing)
	needDedup := tlCfg.Home || tlCfg.Local // dedup needed when any timeline is active
	if tlCfg.Home {
		if err := subscribeChannel(conn, "homeTimeline", "memoh-home"); err != nil {
			return fmt.Errorf("misskey ws connect homeTimeline: %w", err)
		}
		if a.logger != nil {
			a.logger.Info("subscribed to homeTimeline", slog.String("config_id", cfg.ID))
		}
	}
	if tlCfg.Local {
		if err := subscribeChannel(conn, "localTimeline", "memoh-local"); err != nil {
			return fmt.Errorf("misskey ws connect localTimeline: %w", err)
		}
		if a.logger != nil {
			a.logger.Info("subscribed to localTimeline", slog.String("config_id", cfg.ID))
		}
	}

	// Timeline note dedup cache: prevents processing the same note twice
	// when both home and local timelines are active.
	var dedup map[string]time.Time
	var dedupMu sync.Mutex
	if needDedup {
		dedup = make(map[string]time.Time)
		// Periodic cleanup of expired entries.
		cleanupDone := make(chan struct{})
		go func() {
			ticker := time.NewTicker(misskeyDedupCleanupInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-cleanupDone:
					return
				case <-ticker.C:
					dedupMu.Lock()
					now := time.Now()
					for id, ts := range dedup {
						if now.Sub(ts) > misskeyDedupTTL {
							delete(dedup, id)
						}
					}
					dedupMu.Unlock()
				}
			}
		}()
		defer close(cleanupDone)
	}

	// Set initial read deadline and install a pong handler that extends it.
	// If no data (including pong replies) arrives within misskeyReadTimeout,
	// ReadMessage returns an error and the connection is recycled.
	_ = conn.SetReadDeadline(time.Now().Add(misskeyReadTimeout))
	conn.SetPongHandler(func(string) error {
		_ = conn.SetReadDeadline(time.Now().Add(misskeyReadTimeout))
		return nil
	})

	// Start ping ticker.
	pingDone := make(chan struct{})
	go func() {
		ticker := time.NewTicker(misskeyPingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-pingDone:
				return
			case <-ticker.C:
				_ = conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(misskeyWriteTimeout))
			}
		}
	}()
	defer close(pingDone)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		_, msgBytes, readErr := conn.ReadMessage()
		if readErr != nil {
			return fmt.Errorf("misskey ws read: %w", readErr)
		}
		// Extend read deadline on every successful read.
		_ = conn.SetReadDeadline(time.Now().Add(misskeyReadTimeout))
		a.handleStreamMessage(ctx, cfg, me, handler, msgBytes, dedup, &dedupMu, tlCfg)
	}
}

// streamMessage represents a message received from the Misskey streaming API.
type streamMessage struct {
	Type string          `json:"type"`
	Body json.RawMessage `json:"body"`
}

// streamChannelBody is the body of a channel event.
type streamChannelBody struct {
	ID   string          `json:"id"`
	Type string          `json:"type"`
	Body json.RawMessage `json:"body"`
}

// misskeyNote represents a Misskey note (post).
type misskeyNote struct {
	ID         string        `json:"id"`
	Text       string        `json:"text"`
	CW         string        `json:"cw"`
	UserID     string        `json:"userId"`
	User       misskeyUser   `json:"user"`
	ReplyID    string        `json:"replyId"`
	RenoteID   string        `json:"renoteId"`
	CreatedAt  string        `json:"createdAt"`
	Mentions   []string      `json:"mentions"`
	Visibility string        `json:"visibility"`
	Reply      *misskeyNote  `json:"reply"`
	Renote     *misskeyNote  `json:"renote"`
	Files      []misskeyFile `json:"files"`
}

// misskeyFile represents a file attached to a Misskey note.
type misskeyFile struct {
	ID           string           `json:"id"`
	Name         string           `json:"name"`
	Type         string           `json:"type"`
	URL          string           `json:"url"`
	ThumbnailURL string           `json:"thumbnailUrl"`
	Size         int64            `json:"size"`
	IsSensitive  bool             `json:"isSensitive"`
	Properties   misskeyFileProps `json:"properties"`
}

type misskeyFileProps struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

type misskeyUser struct {
	ID        string `json:"id"`
	Username  string `json:"username"`
	Name      string `json:"name"`
	Host      string `json:"host"`
	AvatarURL string `json:"avatarUrl"`
}

// storeMentions caches mention handles from a note so replies can @ 同一串参与者。
// 来源：文本里的 @handles；note.Mentions 仅含 userId 无法直接用。
// 排除：bot 自己、renote 原贴作者（引用原作者）；reply 作者保留。
func (a *MisskeyAdapter) storeMentions(note misskeyNote, me *meResponse) {
	if note.ID == "" {
		return
	}
	mentions := extractMentionHandles(note)

	exclude := map[string]struct{}{}
	if me != nil {
		exclude["@"+me.Username] = struct{}{}
	}
	if note.Renote != nil {
		exclude[formatUserHandle(note.Renote.User)] = struct{}{}
	}

	filtered := make([]string, 0, len(mentions))
	seen := map[string]struct{}{}
	for _, h := range mentions {
		h = strings.TrimSpace(h)
		if h == "" {
			continue
		}
		if _, skip := exclude[h]; skip {
			continue
		}
		if _, dup := seen[h]; dup {
			continue
		}
		seen[h] = struct{}{}
		filtered = append(filtered, h)
	}

	a.mu.Lock()
	a.mentions[note.ID] = filtered
	a.mu.Unlock()
}

func extractMentionHandles(note misskeyNote) []string {
	allText := note.Text
	if note.CW != "" {
		allText += "\n" + note.CW
	}
	if note.Reply != nil {
		allText += "\n" + note.Reply.Text
	}
	if note.Renote != nil {
		allText += "\n" + note.Renote.Text
	}
	mentionRe := regexp.MustCompile(`@[^\s@]+(?:@[^\s@]+)?`)
	return mentionRe.FindAllString(allText, -1)
}

func formatUserHandle(u misskeyUser) string {
	if u.Username == "" {
		return ""
	}
	if strings.TrimSpace(u.Host) != "" {
		return "@" + u.Username + "@" + u.Host
	}
	return "@" + u.Username
}

func (a *MisskeyAdapter) getMentions(noteID string) []string {
	if noteID == "" {
		return nil
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return append([]string(nil), a.mentions[noteID]...)
}

func (a *MisskeyAdapter) handleStreamMessage(ctx context.Context, cfg channel.ChannelConfig, me *meResponse, handler channel.InboundHandler, raw []byte, dedup map[string]time.Time, dedupMu *sync.Mutex, tlCfg timelineConfig) {
	var msg streamMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		return
	}

	if msg.Type == "channel" {
		var body streamChannelBody
		if err := json.Unmarshal(msg.Body, &body); err != nil {
			return
		}
		a.handleChannelEvent(ctx, cfg, me, handler, body, dedup, dedupMu, tlCfg)
	}
}

func (a *MisskeyAdapter) handleChannelEvent(ctx context.Context, cfg channel.ChannelConfig, me *meResponse, handler channel.InboundHandler, body streamChannelBody, dedup map[string]time.Time, dedupMu *sync.Mutex, tlCfg timelineConfig) {
	switch body.Type {
	case "note":
		// Timeline note events from homeTimeline or localTimeline subscriptions.
		if body.ID != "memoh-home" && body.ID != "memoh-local" {
			return
		}
		a.handleTimelineNote(ctx, cfg, me, handler, body, dedup, dedupMu, tlCfg)
		return
	case "mention", "reply":
		var note misskeyNote
		if err := json.Unmarshal(body.Body, &note); err != nil {
			if a.logger != nil {
				a.logger.Warn("parse note failed", slog.String("config_id", cfg.ID), slog.Any("error", err))
			}
			return
		}
		// Dedup: skip if this note was already processed from timeline.
		if dedup != nil && note.ID != "" {
			dedupMu.Lock()
			if ts, seen := dedup[note.ID]; seen && time.Since(ts) < misskeyDedupTTL {
				dedupMu.Unlock()
				return
			}
			dedup[note.ID] = time.Now()
			dedupMu.Unlock()
		}
		// Skip notes from self.
		if note.UserID == me.ID {
			return
		}
		a.storeMentions(note, me)
		inbound, ok := a.buildInboundMessage(me, note)
		if !ok {
			return
		}
		a.logInbound(cfg.ID, inbound)
		go func() {
			if err := handler(ctx, cfg, inbound); err != nil && a.logger != nil {
				a.logger.Error("handle inbound failed", slog.String("config_id", cfg.ID), slog.Any("error", err))
			}
		}()

	case "notification":
		// Handle notification-based mentions.
		var notif struct {
			Type string      `json:"type"`
			Note misskeyNote `json:"note"`
		}
		if err := json.Unmarshal(body.Body, &notif); err != nil {
			return
		}
		if notif.Type != "mention" && notif.Type != "reply" {
			return
		}
		// Dedup: skip if this note was already processed from timeline or channel event.
		if dedup != nil && notif.Note.ID != "" {
			dedupMu.Lock()
			if ts, seen := dedup[notif.Note.ID]; seen && time.Since(ts) < misskeyDedupTTL {
				dedupMu.Unlock()
				return
			}
			dedup[notif.Note.ID] = time.Now()
			dedupMu.Unlock()
		}
		if notif.Note.UserID == me.ID {
			return
		}
		a.storeMentions(notif.Note, me)
		inbound, ok := a.buildInboundMessage(me, notif.Note)
		if !ok {
			return
		}
		a.logInbound(cfg.ID, inbound)
		go func() {
			if err := handler(ctx, cfg, inbound); err != nil && a.logger != nil {
				a.logger.Error("handle inbound failed", slog.String("config_id", cfg.ID), slog.Any("error", err))
			}
		}()
	}
}

func (a *MisskeyAdapter) buildInboundMessage(me *meResponse, note misskeyNote) (channel.InboundMessage, bool) {
	text := strings.TrimSpace(note.Text)
	attachments := collectMisskeyAttachments(note)

	// If this is a quote/renote without commentary, fall back to the quoted text so we don't drop it.
	if text == "" && note.Renote != nil {
		if rt := strings.TrimSpace(note.Renote.Text); rt != "" {
			text = rt
		}
	}

	if text == "" && len(attachments) == 0 {
		return channel.InboundMessage{}, false
	}

	// Strip the bot mention from the text.
	if me != nil {
		mention := "@" + me.Username
		text = strings.TrimSpace(strings.Replace(text, mention, "", 1))
	}
	if text == "" {
		return channel.InboundMessage{}, false
	}

	// Build quoted context for replies.
	if note.Reply != nil && note.Reply.Text != "" {
		quotedText := strings.TrimSpace(note.Reply.Text)
		if len([]rune(quotedText)) > 200 {
			quotedText = string([]rune(quotedText)[:200]) + "..."
		}
		senderName := note.Reply.User.Name
		if senderName == "" {
			senderName = note.Reply.User.Username
		}
		if senderName != "" {
			text = fmt.Sprintf("[Reply to %s: %s]\n%s", senderName, quotedText, text)
		}
	}

	// Build quoted context for renotes (quote posts).
	if note.Renote != nil && note.Renote.Text != "" {
		quotedText := strings.TrimSpace(note.Renote.Text)
		if len([]rune(quotedText)) > 200 {
			quotedText = string([]rune(quotedText)[:200]) + "..."
		}
		senderName := note.Renote.User.Name
		if senderName == "" {
			senderName = note.Renote.User.Username
		}
		if senderName != "" {
			if text == "" {
				text = fmt.Sprintf("[Renote from %s: %s]", senderName, quotedText)
			} else {
				text = fmt.Sprintf("[Renote from %s: %s]\n%s", senderName, quotedText, text)
			}
		}
	}

	senderID := note.UserID
	displayName := note.User.Name
	if displayName == "" {
		displayName = note.User.Username
	}
	attrs := map[string]string{
		"user_id":  note.UserID,
		"username": note.User.Username,
	}
	if note.User.Host != "" {
		attrs["host"] = note.User.Host
	}

	// Direct messages use "specified" visibility; others are group conversations.
	convType := channel.ConversationTypeGroup
	if note.Visibility == "specified" {
		convType = channel.ConversationTypePrivate
	}

	var replyRef *channel.ReplyRef
	if note.ReplyID != "" {
		replyRef = &channel.ReplyRef{
			MessageID: note.ReplyID,
		}
	}

	receivedAt := time.Now().UTC()
	if note.CreatedAt != "" {
		if t, err := time.Parse(time.RFC3339, note.CreatedAt); err == nil {
			receivedAt = t
		}
	}

	isMentioned := false
	if me != nil {
		for _, mid := range note.Mentions {
			if mid == me.ID {
				isMentioned = true
				break
			}
		}
	}

	return channel.InboundMessage{
		Channel: Type,
		Message: channel.Message{
			ID:          note.ID,
			Format:      channel.MessageFormatPlain,
			Text:        text,
			Reply:       replyRef,
			Attachments: attachments,
		},
		ReplyTarget: note.ID,
		Sender: channel.Identity{
			SubjectID:   senderID,
			DisplayName: displayName,
			Attributes:  attrs,
		},
		Conversation: channel.Conversation{
			ID:   note.UserID,
			Type: convType,
		},
		ReceivedAt: receivedAt,
		Source:     "misskey",
		Metadata: map[string]any{
			"is_mentioned": isMentioned,
			"visibility":   note.Visibility,
			"note_id":      note.ID,
		},
	}, true
}

// handleTimelineNote processes a note event from a timeline subscription.
func (a *MisskeyAdapter) handleTimelineNote(ctx context.Context, cfg channel.ChannelConfig, me *meResponse, handler channel.InboundHandler, body streamChannelBody, dedup map[string]time.Time, dedupMu *sync.Mutex, tlCfg timelineConfig) {
	var note misskeyNote
	if err := json.Unmarshal(body.Body, &note); err != nil {
		if a.logger != nil {
			a.logger.Warn("parse timeline note failed", slog.String("config_id", cfg.ID), slog.Any("error", err))
		}
		return
	}

	// Dedup: when both home and local timelines are active, the same note
	// can arrive from both channels. Only process it once.
	if dedup != nil {
		dedupMu.Lock()
		if ts, seen := dedup[note.ID]; seen && time.Since(ts) < misskeyDedupTTL {
			dedupMu.Unlock()
			return
		}
		dedup[note.ID] = time.Now()
		dedupMu.Unlock()
	}

	// Basic filtering: skip noise.
	if note.UserID == me.ID {
		return // own notes
	}
	// Skip pure renotes (reposts without commentary).
	if note.RenoteID != "" && strings.TrimSpace(note.Text) == "" {
		return
	}
	text := strings.TrimSpace(note.Text)
	if text == "" && len(note.Files) == 0 {
		return // empty note
	}
	if text != "" && len([]rune(text)) < misskeyTimelineMinTextLen {
		return // too short (but allow file-only notes through)
	}
	if note.Visibility == "specified" {
		return // DM, not timeline material
	}

	source := "home"
	if body.ID == "memoh-local" {
		source = "local"
	}

	inbound := a.buildTimelineInboundMessage(note, source, tlCfg.Discuss)
	a.logInbound(cfg.ID, inbound)
	go func() {
		if err := handler(ctx, cfg, inbound); err != nil && a.logger != nil {
			a.logger.Error("handle timeline inbound failed", slog.String("config_id", cfg.ID), slog.Any("error", err))
		}
	}()
}

// buildTimelineInboundMessage creates an InboundMessage from a timeline note.
func (*MisskeyAdapter) buildTimelineInboundMessage(note misskeyNote, source string, discuss bool) channel.InboundMessage {
	text := strings.TrimSpace(note.Text)
	attachments := collectMisskeyAttachments(note)

	// Prefix with timeline context so the LLM understands where this came from.
	username := note.User.Username
	displayName := note.User.Name
	if displayName == "" {
		displayName = username
	}
	if text != "" {
		text = fmt.Sprintf("[Timeline/%s] @%s: %s", source, username, text)
	} else if len(attachments) > 0 {
		text = fmt.Sprintf("[Timeline/%s] @%s: [shared %d file(s)]", source, username, len(attachments))
	}

	attrs := map[string]string{
		"user_id":  note.UserID,
		"username": note.User.Username,
	}
	if note.User.Host != "" {
		attrs["host"] = note.User.Host
	}

	receivedAt := time.Now().UTC()
	if note.CreatedAt != "" {
		if t, err := time.Parse(time.RFC3339, note.CreatedAt); err == nil {
			receivedAt = t
		}
	}

	// All timeline notes aggregate into a single conversation.
	convID := "timeline"

	return channel.InboundMessage{
		Channel: Type,
		Message: channel.Message{
			ID:          note.ID,
			Format:      channel.MessageFormatPlain,
			Text:        text,
			Attachments: attachments,
		},
		ReplyTarget: note.ID,
		Sender: channel.Identity{
			SubjectID:   note.UserID,
			DisplayName: displayName,
			Attributes:  attrs,
		},
		Conversation: channel.Conversation{
			ID:   convID,
			Type: channel.ConversationTypeGroup,
		},
		ReceivedAt: receivedAt,
		Source:     "misskey",
		Metadata: map[string]any{
			"is_timeline":         true,
			"is_mentioned":        false,
			"is_discuss_timeline": discuss,
			"timeline_source":     source,
			"visibility":          note.Visibility,
			"note_id":             note.ID,
		},
	}
}

// collectMisskeyAttachments extracts channel.Attachment slice from a Misskey note's files.
func collectMisskeyAttachments(note misskeyNote) []channel.Attachment {
	if len(note.Files) == 0 {
		return nil
	}
	attachments := make([]channel.Attachment, 0, len(note.Files))
	for _, f := range note.Files {
		if f.URL == "" && f.Type == "" {
			continue
		}
		att := channel.Attachment{
			Type:           mapMisskeyAttachmentType(f.Type),
			URL:            f.URL,
			Name:           f.Name,
			Mime:           f.Type,
			Size:           f.Size,
			Width:          f.Properties.Width,
			Height:         f.Properties.Height,
			ThumbnailURL:   f.ThumbnailURL,
			SourcePlatform: "misskey",
		}
		attachments = append(attachments, channel.NormalizeInboundChannelAttachment(att))
	}
	return attachments
}

// mapMisskeyAttachmentType maps a Misskey file MIME type to a channel AttachmentType.
func mapMisskeyAttachmentType(mime string) channel.AttachmentType {
	switch {
	case strings.HasPrefix(mime, "image/gif"):
		return channel.AttachmentGIF
	case strings.HasPrefix(mime, "image/"):
		return channel.AttachmentImage
	case strings.HasPrefix(mime, "video/"):
		return channel.AttachmentVideo
	case strings.HasPrefix(mime, "audio/"):
		return channel.AttachmentAudio
	default:
		return channel.AttachmentFile
	}
}

func (a *MisskeyAdapter) logInbound(configID string, msg channel.InboundMessage) {
	if a.logger == nil {
		return
	}
	a.logger.Info("inbound received",
		slog.String("config_id", configID),
		slog.String("user_id", msg.Sender.Attribute("user_id")),
		slog.String("username", msg.Sender.Attribute("username")),
		slog.String("text", common.SummarizeText(msg.Message.Text)),
	)
}

// --- Sender ---

// Send delivers an outbound message to Misskey by creating a note.
func (a *MisskeyAdapter) Send(ctx context.Context, cfg channel.ChannelConfig, msg channel.PreparedOutboundMessage) error {
	mkCfg, err := parseConfig(cfg.Credentials)
	if err != nil {
		return err
	}
	text := strings.TrimSpace(msg.Message.Message.PlainText())
	if text == "" && len(msg.Message.Attachments) == 0 {
		return errors.New("message text or attachments required")
	}

	// Extract replyId from the message's Reply reference, not from target.
	// Target is the delivery destination (user ID for DMs), replyId is the
	// note being replied to.
	var replyID string
	if msg.Message.Message.Reply != nil {
		replyID = strings.TrimSpace(msg.Message.Message.Reply.MessageID)
	}

	// Auto-mention: prepend all participants from the replied note, excluding self/reply/renote authors.
	if replyID != "" {
		if auto := a.getMentions(replyID); len(auto) > 0 {
			prefix := strings.Join(auto, " ")
			if text == "" {
				text = prefix
			} else {
				text = prefix + "\n" + text
			}
		}
	}

	text = textutil.TruncateRunesWithSuffix(text, misskeyMaxNoteLength, "...")

	// Determine visibility: use "home" for replies, "public" for standalone notes.
	visibility := "public"
	if replyID != "" {
		visibility = "home"
	}

	// Upload attachments to Misskey Drive and collect file IDs.
	fileIDs, err := a.uploadAttachments(ctx, mkCfg, msg.Message.Attachments)
	if err != nil && a.logger != nil {
		a.logger.Warn("misskey: some attachments failed to upload", slog.Any("error", err))
	}

	_, err = createNote(ctx, mkCfg, text, replyID, visibility, fileIDs...)
	if err != nil {
		if a.logger != nil {
			a.logger.Error("send note failed", slog.String("config_id", cfg.ID), slog.Any("error", err))
		}
		return err
	}
	return nil
}

// uploadAttachments uploads prepared attachments to Misskey Drive and returns
// the resulting file IDs. Failed uploads are skipped silently.
func (a *MisskeyAdapter) uploadAttachments(ctx context.Context, cfg Config, attachments []channel.PreparedAttachment) ([]string, error) {
	if len(attachments) == 0 {
		return nil, nil
	}
	var fileIDs []string
	var firstErr error
	for _, att := range attachments {
		if att.Open == nil {
			continue
		}
		reader, err := att.Open(ctx)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("open attachment %s: %w", att.Name, err)
			}
			continue
		}
		filename := att.Name
		if filename == "" {
			filename = "upload"
			if ext := mimeToExt(att.Mime); ext != "" {
				filename += ext
			}
		}
		driveFile, err := uploadToDrive(ctx, cfg, reader, filename, att.Mime)
		_ = reader.Close()
		if err != nil {
			if a.logger != nil {
				a.logger.Warn("misskey: drive upload failed", slog.String("name", filename), slog.Any("error", err))
			}
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if a.logger != nil {
			a.logger.Debug("misskey: file uploaded to drive", slog.String("id", driveFile.ID), slog.String("name", driveFile.Name))
		}
		fileIDs = append(fileIDs, driveFile.ID)
	}
	return fileIDs, firstErr
}

// mimeToExt returns a common file extension for the given MIME type.
func mimeToExt(mime string) string {
	switch strings.ToLower(mime) {
	case "image/png":
		return ".png"
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/svg+xml":
		return ".svg"
	case "video/mp4":
		return ".mp4"
	case "audio/mpeg":
		return ".mp3"
	default:
		return ""
	}
}

// --- Reactor ---

// React adds an emoji reaction to a message (implements channel.Reactor).
func (*MisskeyAdapter) React(ctx context.Context, cfg channel.ChannelConfig, _ string, messageID string, emoji string) error {
	mkCfg, err := parseConfig(cfg.Credentials)
	if err != nil {
		return err
	}
	// Misskey reactions use format like ":emoji:" or unicode emoji.
	if !strings.HasPrefix(emoji, ":") {
		emoji = ":" + emoji + ":"
	}
	return createReaction(ctx, mkCfg, messageID, emoji)
}

// Unreact removes the bot's reaction from a message (implements channel.Reactor).
func (*MisskeyAdapter) Unreact(ctx context.Context, cfg channel.ChannelConfig, _ string, messageID string, _ string) error {
	mkCfg, err := parseConfig(cfg.Credentials)
	if err != nil {
		return err
	}
	return deleteReaction(ctx, mkCfg, messageID)
}

// --- ProcessingStatusNotifier ---

// ProcessingStarted is a no-op for Misskey (no typing indicator API).
func (*MisskeyAdapter) ProcessingStarted(_ context.Context, _ channel.ChannelConfig, _ channel.InboundMessage, _ channel.ProcessingStatusInfo) (channel.ProcessingStatusHandle, error) {
	return channel.ProcessingStatusHandle{}, nil
}

// ProcessingCompleted is a no-op for Misskey.
func (*MisskeyAdapter) ProcessingCompleted(_ context.Context, _ channel.ChannelConfig, _ channel.InboundMessage, _ channel.ProcessingStatusInfo, _ channel.ProcessingStatusHandle) error {
	return nil
}

// ProcessingFailed is a no-op for Misskey.
func (*MisskeyAdapter) ProcessingFailed(_ context.Context, _ channel.ChannelConfig, _ channel.InboundMessage, _ channel.ProcessingStatusInfo, _ channel.ProcessingStatusHandle, _ error) error {
	return nil
}

// --- StreamSender (block-streaming: buffer deltas, send final as one message) ---

// OpenStream opens a block-streaming session that buffers all deltas and sends
// the final message as a single note when the stream is closed.
func (a *MisskeyAdapter) OpenStream(_ context.Context, cfg channel.ChannelConfig, target string, _ channel.StreamOptions) (channel.PreparedOutboundStream, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return nil, errors.New("misskey target is required")
	}
	return &misskeyBlockStream{
		adapter: a,
		cfg:     cfg,
		target:  target,
	}, nil
}

// misskeyBlockStream buffers streaming deltas and sends the final message as
// one Send call when the stream is closed.
type misskeyBlockStream struct {
	adapter     *MisskeyAdapter
	cfg         channel.ChannelConfig
	target      string
	textBuilder strings.Builder
	attachments []channel.PreparedAttachment
	final       *channel.PreparedMessage
	closed      bool
}

func (s *misskeyBlockStream) Push(_ context.Context, event channel.PreparedStreamEvent) error {
	if s.closed {
		return nil
	}
	switch event.Type {
	case channel.StreamEventDelta:
		if strings.TrimSpace(event.Delta) != "" && event.Phase != channel.StreamPhaseReasoning {
			s.textBuilder.WriteString(event.Delta)
		}
	case channel.StreamEventAttachment:
		s.attachments = append(s.attachments, event.Attachments...)
	case channel.StreamEventFinal:
		if event.Final != nil {
			msg := event.Final.Message
			s.final = &msg
		}
	}
	return nil
}

// --- Timeline configuration ---

// timelineConfig holds timeline subscription settings parsed from routing.
type timelineConfig struct {
	Home    bool `json:"home"`
	Local   bool `json:"local"`
	Discuss bool `json:"discuss"` // use discuss mode for timeline sessions (smart timing)
}

// parseTimelineConfig extracts timeline settings from the routing config.
func parseTimelineConfig(routing map[string]any) timelineConfig {
	if routing == nil {
		return timelineConfig{}
	}
	var cfg timelineConfig
	tl, ok := routing["timeline"]
	if !ok {
		return timelineConfig{}
	}
	v, ok := tl.(map[string]any)
	if !ok {
		return timelineConfig{}
	}
	if home, ok := v["home"]; ok {
		cfg.Home = toBool(home)
	}
	if local, ok := v["local"]; ok {
		cfg.Local = toBool(local)
	}
	if discuss, ok := v["discuss"]; ok {
		cfg.Discuss = toBool(discuss)
	}
	return cfg
}

func toBool(v any) bool {
	switch b := v.(type) {
	case bool:
		return b
	case string:
		return strings.EqualFold(b, "true") || b == "1"
	case json.Number:
		return b.String() == "1"
	case float64:
		return b == 1
	default:
		return false
	}
}

// subscribeChannel sends a channel connect message over the WebSocket.
func subscribeChannel(conn *websocket.Conn, channelName, id string) error {
	return conn.WriteJSON(map[string]any{
		"type": "connect",
		"body": map[string]any{
			"channel": channelName,
			"id":      id,
		},
	})
}

func (s *misskeyBlockStream) Close(ctx context.Context) error {
	if s.closed {
		return nil
	}
	s.closed = true

	msg := channel.PreparedMessage{Message: channel.Message{Format: channel.MessageFormatPlain}}
	if s.final != nil {
		msg = *s.final
	}
	if strings.TrimSpace(msg.Message.Text) == "" {
		msg.Message.Text = strings.TrimSpace(s.textBuilder.String())
	}
	// Filter out raw JSON reasoning arrays that some APIs (e.g. Zhipu/GLM)
	// incorrectly emit in the content field when context overflows.
	msg.Message.Text = channel.FilterReasoningArray(msg.Message.Text)
	msg.Message.Text = channel.FilterThinkingTags(msg.Message.Text)
	msg.Message.Text = channel.FilterToolCallXML(msg.Message.Text)
	if len(msg.Attachments) == 0 && len(s.attachments) > 0 {
		msg.Attachments = append(msg.Attachments, s.attachments...)
	}
	if msg.Message.IsEmpty() {
		return nil
	}
	// Set reply reference from the stream target (the inbound note ID).
	if s.target != "" && msg.Message.Reply == nil {
		msg.Message.Reply = &channel.ReplyRef{MessageID: s.target}
	}
	return s.adapter.Send(ctx, s.cfg, channel.PreparedOutboundMessage{
		Target:  s.target,
		Message: msg,
	})
}
