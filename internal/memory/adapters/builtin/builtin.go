package builtin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/memohai/memoh/internal/conversation"
	"github.com/memohai/memoh/internal/mcp"
	adapters "github.com/memohai/memoh/internal/memory/adapters"
	"github.com/memohai/memoh/internal/memory/scene"
	"github.com/memohai/memoh/internal/memory/working"
)

const (
	BuiltinType = "builtin"

	sharedMemoryNamespace = "bot"

	defaultMemoryToolLimit = 8
	maxMemoryToolLimit     = 50
	toolSearchMemory       = "search_memory"

	// maxSearchCallsPerTurn limits how many times search_memory can be called in a single turn.
	maxSearchCallsPerTurn = 5
)

// ProfileSummaryProvider abstracts the profile service for summary retrieval.
// This avoids a direct dependency on the profiles package.
type ProfileSummaryProvider interface {
	GetSummary(ctx context.Context, botID, userID string) (string, error)
}

// BuiltinProvider wraps the existing Service as a Provider.
type BuiltinProvider struct {
	service        memoryRuntime
	llm            adapters.LLM
	chatAccessor   conversation.Accessor
	adminChecker   AdminChecker
	workingMem     *working.WorkingMemory
	profileService ProfileSummaryProvider
	sceneStore     scene.Store
	pipeline       *FormationPipeline
	logger         *slog.Logger
	packer         contextPackerConfig
	cb             *CircuitBreaker // circuit breaker for memory search
}

// memoryRuntime is the runtime memory backend required by the builtin provider.
// It is intentionally defined as an interface to decouple provider wiring from
// concrete service structs in the memory package.
type memoryRuntime interface {
	Add(ctx context.Context, req adapters.AddRequest) (adapters.SearchResponse, error)
	Search(ctx context.Context, req adapters.SearchRequest) (adapters.SearchResponse, error)
	GetAll(ctx context.Context, req adapters.GetAllRequest) (adapters.SearchResponse, error)
	Update(ctx context.Context, req adapters.UpdateRequest) (adapters.MemoryItem, error)
	Delete(ctx context.Context, memoryID string) (adapters.DeleteResponse, error)
	DeleteBatch(ctx context.Context, memoryIDs []string) (adapters.DeleteResponse, error)
	DeleteAll(ctx context.Context, req adapters.DeleteAllRequest) (adapters.DeleteResponse, error)
	Compact(ctx context.Context, filters map[string]any, ratio float64, decayDays int) (adapters.CompactResult, error)
	Usage(ctx context.Context, filters map[string]any) (adapters.UsageResponse, error)
	Mode() string
	Status(ctx context.Context, botID string) (adapters.MemoryStatusResponse, error)
	Rebuild(ctx context.Context, botID string) (adapters.RebuildResult, error)
}

// AdminChecker checks whether a channel identity has admin privileges.
type AdminChecker interface {
	IsAdmin(ctx context.Context, channelIdentityID string) (bool, error)
}

func NewBuiltinProvider(log *slog.Logger, service any, chatAccessor conversation.Accessor, adminChecker AdminChecker) *BuiltinProvider {
	if log == nil {
		log = slog.Default()
	}
	logger := log.With(slog.String("provider", BuiltinType))
	runtimeService, ok := service.(memoryRuntime)
	if service != nil && !ok {
		logger.Warn("service does not implement memoryRuntime; provider will operate without a backend")
	}
	return &BuiltinProvider{
		service:      runtimeService,
		chatAccessor: chatAccessor,
		adminChecker: adminChecker,
		logger:       logger,
		packer:       defaultPackerConfig,
		cb: NewCircuitBreaker(logger, CircuitBreakerConfig{
			FailureThreshold: defaultFailureThreshold,
			OpenDuration:     defaultOpenDuration,
		}),
	}
}

// SetLLM injects the LLM client used for Extract/Decide in memory formation.
// When an LLM is set, the formation pipeline is automatically initialized.
func (p *BuiltinProvider) SetLLM(llm adapters.LLM) {
	p.llm = llm
	// Initialize the formation pipeline when LLM is available.
	if llm != nil && p.pipeline == nil {
		p.pipeline = NewFormationPipeline(p.logger, func(ctx context.Context, req adapters.AfterChatRequest) formationResult {
			return runFormation(ctx, p.logger, p.llm, p.service, req)
		})
	}
}

// FlushPipeline synchronously processes all buffered pipeline messages.
// Exposed for testing and graceful shutdown scenarios.
func (p *BuiltinProvider) FlushPipeline() {
	if p.pipeline != nil {
		p.pipeline.Flush()
	}
}

// SetWorkingMemory injects the working memory cache for short-term recall.
func (p *BuiltinProvider) SetWorkingMemory(wm *working.WorkingMemory) {
	p.workingMem = wm
}

// SetProfileService injects the profile service for user profile summary injection.
func (p *BuiltinProvider) SetProfileService(ps ProfileSummaryProvider) {
	p.profileService = ps
}

// SetSceneStore injects the scene store for scene navigation and search.
func (p *BuiltinProvider) SetSceneStore(store scene.Store) {
	p.sceneStore = store
}

// SetPackerConfig overrides the default context packing configuration.
// Zero-valued fields fall back to defaults.
func (p *BuiltinProvider) SetPackerConfig(cfg contextPackerConfig) {
	if cfg.TargetItems > 0 {
		p.packer.TargetItems = cfg.TargetItems
	}
	if cfg.MaxTotalChars > 0 {
		p.packer.MaxTotalChars = cfg.MaxTotalChars
	}
	if cfg.MinItemChars > 0 {
		p.packer.MinItemChars = cfg.MinItemChars
	}
	if cfg.MaxItemChars > 0 {
		p.packer.MaxItemChars = cfg.MaxItemChars
	}
	if cfg.OverfetchRatio > 0 {
		p.packer.OverfetchRatio = cfg.OverfetchRatio
	}
}

// ApplyProviderConfig reads context packing knobs from a provider config map
// and applies any non-zero values to the provider's packer configuration.
func (p *BuiltinProvider) ApplyProviderConfig(providerConfig map[string]any) {
	p.SetPackerConfig(contextPackerConfig{
		TargetItems:   intFromConfig(providerConfig, "context_target_items"),
		MaxTotalChars: intFromConfig(providerConfig, "context_max_total_chars"),
	})
}

func intFromConfig(m map[string]any, key string) int {
	if m == nil {
		return 0
	}
	v, ok := m[key]
	if !ok || v == nil {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	}
	return 0
}

func (*BuiltinProvider) Type() string { return BuiltinType }

func memorySourceLabel(item adapters.MemoryItem) string {
	var parts []string
	if item.Metadata != nil {
		if name, ok := item.Metadata["profile_display_name"].(string); ok {
			name = strings.TrimSpace(name)
			if name != "" {
				parts = append(parts, name)
			}
		}
		if platform, ok := item.Metadata["source_platform"].(string); ok {
			platform = strings.TrimSpace(platform)
			if platform != "" {
				parts = append(parts, platform)
			}
		}
	}
	if ts := strings.TrimSpace(item.CreatedAt); ts != "" {
		if len(ts) > 10 {
			ts = ts[:10]
		}
		parts = append(parts, ts)
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, ", ")
}

// --- Conversation Hooks ---

func (p *BuiltinProvider) OnBeforeChat(ctx context.Context, req adapters.BeforeChatRequest) (*adapters.BeforeChatResult, error) {
	if p.service == nil {
		return nil, nil
	}
	if strings.TrimSpace(req.Query) == "" || strings.TrimSpace(req.BotID) == "" {
		return nil, nil
	}

	var contextParts []string

	// 1. Working memory (short-term, recent facts from LRU).
	if p.workingMem != nil {
		wmEntries := p.workingMem.Search(req.BotID, req.Query, 5)
		if len(wmEntries) > 0 {
			var wmSB strings.Builder
			wmSB.WriteString("<working-memory>\n")
			for _, e := range wmEntries {
				wmSB.WriteString("- ")
				wmSB.WriteString(e.Content)
				if e.Importance == "high" {
					wmSB.WriteString(" [important]")
				}
				wmSB.WriteString("\n")
			}
			wmSB.WriteString("</working-memory>")
			contextParts = append(contextParts, wmSB.String())
		}
	}

	// 2. Long-term memory from Qdrant (with timeout and circuit breaker).
	if p.cb.Allow() {
		const searchTimeout = 5 * time.Second
		searchCtx, searchCancel := context.WithTimeout(ctx, searchTimeout)
		defer searchCancel()

		fetchLimit := overfetchLimit(p.packer)
		resp, err := p.service.Search(searchCtx, adapters.SearchRequest{
			Query: req.Query,
			BotID: req.BotID,
			Limit: fetchLimit,
			Filters: map[string]any{
				"namespace": sharedMemoryNamespace,
				"scopeId":   req.BotID,
				"bot_id":    req.BotID,
			},
			NoStats: true,
		})
		if err != nil {
			switch {
			case ctx.Err() != nil:
				// Parent context cancelled, don't count as circuit failure
				p.logger.Warn("memory search cancelled by parent context", slog.Any("error", err))
			case searchCtx.Err() != nil:
				// Search-specific timeout
				p.cb.RecordFailure()
				p.logger.Warn("memory search timed out",
					slog.String("bot_id", req.BotID),
					slog.Duration("timeout", searchTimeout),
				)
			default:
				p.cb.RecordFailure()
				p.logger.Warn("memory search for context failed", slog.Any("error", err))
			}
		} else {
			p.cb.RecordSuccess()
			candidates := deduplicateAndSort(resp.Results)
			if len(candidates) > 0 {
				packed := packContext(candidates, p.packer)
				if len(packed.Items) > 0 {
					var sb strings.Builder
					sb.WriteString("<memory-context>\nRelevant memory context (use when helpful):\n")
					for _, entry := range packed.Items {
						sb.WriteString("- ")
						if label := memorySourceLabel(entry.Item); label != "" {
							sb.WriteString("[")
							sb.WriteString(label)
							sb.WriteString("] ")
						}
						sb.WriteString(entry.Snippet)
						sb.WriteString("\n")
					}
					sb.WriteString("</memory-context>")
					contextParts = append(contextParts, sb.String())
				}
			}
		}
	} else {
		p.logger.Warn("memory search skipped: circuit breaker open",
			slog.String("bot_id", req.BotID),
		)
	}

	if len(contextParts) == 0 {
		return nil, nil
	}

	// Assemble partitioned context output.
	prependUserContext := strings.Join(contextParts, "\n")

	// AppendSystemContext holds stable content (profile, scene index) that
	// benefits from prompt caching. Populated by Profile injection (task 5)
	// and scene index injection (task 11).
	var systemContextParts []string
	// Profile summary injection: inject user profile into system context.
	if p.profileService != nil && strings.TrimSpace(req.UserID) != "" {
		summary, err := p.profileService.GetSummary(ctx, req.BotID, req.UserID)
		if err != nil {
			p.logger.Warn("profile summary retrieval failed",
				slog.String("bot_id", req.BotID),
				slog.String("user_id", req.UserID),
				slog.Any("error", err),
			)
		} else if strings.TrimSpace(summary) != "" {
			systemContextParts = append(systemContextParts, "<user-profile>\n"+summary+"\n</user-profile>")
		}
	}
	// Scene navigation index injection.
	if p.sceneStore != nil {
		scenes, err := p.sceneStore.List(ctx, req.BotID)
		if err != nil {
			p.logger.Warn("scene index retrieval failed",
				slog.String("bot_id", req.BotID),
				slog.Any("error", err),
			)
		} else if len(scenes) > 0 {
			var sb strings.Builder
			sb.WriteString("<scene-index>\n")
			for _, sc := range scenes {
				fmt.Fprintf(&sb, "- [%s] %s: %s\n", sc.ID, sc.Title, sc.Summary)
			}
			sb.WriteString("</scene-index>")
			systemContextParts = append(systemContextParts, sb.String())
		}
	}
	appendSystemContext := strings.Join(systemContextParts, "\n")

	return &adapters.BeforeChatResult{
		ContextText:         prependUserContext, // backward compat
		AppendSystemContext: appendSystemContext,
		PrependUserContext:  prependUserContext,
	}, nil
}

func (p *BuiltinProvider) OnAfterChat(ctx context.Context, req adapters.AfterChatRequest) error {
	if p.service == nil {
		return nil
	}
	botID := strings.TrimSpace(req.BotID)
	if botID == "" {
		return nil
	}
	if len(req.Messages) == 0 {
		return nil
	}

	if p.llm != nil {
		// Sync to working memory immediately (fast, no LLM call).
		p.syncToWorkingMemory(botID, req)

		// Enqueue to pipeline for batched formation instead of running synchronously.
		if p.pipeline != nil {
			p.pipeline.Enqueue(req) //nolint:contextcheck // Pipeline processes asynchronously with its own context
			p.logger.Debug("memory formation enqueued to pipeline",
				slog.String("bot_id", botID),
				slog.Int("buffer_size", p.pipeline.BufferSize()),
				slog.Int("threshold", p.pipeline.Threshold()),
			)
		} else {
			// Fallback: no pipeline configured, run formation synchronously (legacy).
			result := runFormation(ctx, p.logger, p.llm, p.service, req)
			p.logger.Debug("memory formation completed",
				slog.String("bot_id", botID),
				slog.Int("extracted", result.ExtractedFacts),
				slog.Int("added", result.Added),
				slog.Int("updated", result.Updated),
				slog.Int("deleted", result.Deleted),
				slog.Int("skipped", result.Skipped),
			)
		}

		// Promote high-access working memory entries to long-term Qdrant.
		p.promoteWorkingMemory(ctx, botID)
		return nil
	}

	// Fallback: no LLM configured, store raw transcript (legacy path).
	filters := map[string]any{
		"namespace": sharedMemoryNamespace,
		"scopeId":   botID,
		"bot_id":    botID,
	}
	metadata := adapters.BuildProfileMetadata(req.UserID, req.ChannelIdentityID, req.DisplayName)
	if req.SourcePlatform != "" || req.SourceSessionID != "" {
		if metadata == nil {
			metadata = make(map[string]any)
		}
		if req.SourcePlatform != "" {
			metadata["source_platform"] = req.SourcePlatform
		}
		if req.SourceSessionID != "" {
			metadata["source_session_id"] = req.SourceSessionID
		}
	}
	if _, err := p.service.Add(ctx, adapters.AddRequest{
		Messages: req.Messages,
		BotID:    botID,
		Metadata: metadata,
		Filters:  filters,
	}); err != nil {
		p.logger.Warn("store memory failed", slog.String("bot_id", botID), slog.Any("error", err))
	}
	return nil
}

// syncToWorkingMemory extracts the latest user/assistant messages and adds
// them to the working memory cache for short-term recall.
func (p *BuiltinProvider) syncToWorkingMemory(botID string, req adapters.AfterChatRequest) {
	if p.workingMem == nil {
		return
	}
	for _, msg := range req.Messages {
		content := strings.TrimSpace(msg.Content)
		if content == "" {
			continue
		}
		// Use basic importance: assistant messages are higher priority
		importance := "medium"
		if msg.Role == "assistant" {
			importance = "low" // assistant output is less important for memory
		}
		p.workingMem.Add(botID, content, importance, map[string]any{
			"role": msg.Role,
		})
	}
}

// promoteWorkingMemory scans the working memory for entries with high access
// counts and non-low importance, then promotes them to long-term Qdrant storage.
func (p *BuiltinProvider) promoteWorkingMemory(ctx context.Context, botID string) {
	if p.workingMem == nil {
		return
	}
	const minAccess = 3
	entries := p.workingMem.GetHighAccessEntries(botID, minAccess)
	if len(entries) == 0 {
		return
	}

	filters := map[string]any{
		"namespace": sharedMemoryNamespace,
		"scopeId":   botID,
		"bot_id":    botID,
	}

	promotedCount := 0
	for _, entry := range entries {
		// Dedup check: search for existing similar content before promoting.
		if resp, err := p.service.Search(ctx, adapters.SearchRequest{
			Query:   entry.Content,
			BotID:   botID,
			Limit:   1,
			Filters: filters,
			NoStats: true,
		}); err == nil && len(resp.Results) > 0 && resp.Results[0].Score > 0.9 {
			p.logger.Debug("promote skipped: similar memory already exists",
				slog.String("bot_id", botID),
				slog.String("existing_id", resp.Results[0].ID),
			)
			continue
		}

		meta := make(map[string]any)
		if entry.Metadata != nil {
			for k, v := range entry.Metadata {
				meta[k] = v
			}
		}
		meta["source"] = "working_memory"
		meta["promoted_at"] = time.Now().UTC().Format(time.RFC3339)
		meta["access_count"] = entry.AccessCount
		meta["importance"] = entry.Importance

		if _, err := p.service.Add(ctx, adapters.AddRequest{
			Message:  entry.Content,
			BotID:    botID,
			Metadata: meta,
			Filters:  filters,
		}); err != nil {
			p.logger.Warn("promote working memory to Qdrant failed",
				slog.String("bot_id", botID),
				slog.String("content", entry.Content[:min(len(entry.Content), 50)]),
				slog.Any("error", err),
			)
			continue
		}
		promotedCount++
	}

	if promotedCount > 0 {
		p.logger.Info("promoted working memory entries to long-term storage",
			slog.String("bot_id", botID),
			slog.Int("promoted", promotedCount),
		)
	}
}

// --- MCP Tools ---

func (p *BuiltinProvider) ListTools(_ context.Context, _ mcp.ToolSessionContext) ([]mcp.ToolDescriptor, error) {
	if p.service == nil {
		return []mcp.ToolDescriptor{}, nil
	}
	return []mcp.ToolDescriptor{
		{
			Name:        toolSearchMemory,
			Description: "Search for memories relevant to the current chat. Supports semantic search, time-range filtering, and scene-based retrieval.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{
						"type":        "string",
						"description": "The query to search memories",
					},
					"limit": map[string]any{
						"type":        "integer",
						"description": "Maximum number of memory results",
					},
					"mode": map[string]any{
						"type":        "string",
						"enum":        []string{"search", "time", "scene"},
						"description": "Search mode: 'search' (default, semantic), 'time' (time-range filter), 'scene' (retrieve scene and associated memories)",
					},
					"after": map[string]any{
						"type":        "string",
						"description": "For mode=time: only return memories after this date (ISO 8601, e.g. 2024-01-01)",
					},
					"before": map[string]any{
						"type":        "string",
						"description": "For mode=time: only return memories before this date (ISO 8601, e.g. 2024-12-31)",
					},
				},
				"required": []string{"query"},
			},
		},
	}, nil
}

func (p *BuiltinProvider) CallTool(ctx context.Context, session mcp.ToolSessionContext, toolName string, arguments map[string]any) (map[string]any, error) {
	if toolName != toolSearchMemory {
		return nil, mcp.ErrToolNotFound
	}
	if p.service == nil {
		return mcp.BuildToolErrorResult("memory service not available"), nil
	}

	// Enforce per-turn call count limit.
	if session.ToolCallCount >= maxSearchCallsPerTurn {
		return mcp.BuildToolErrorResult(fmt.Sprintf("search_memory call limit reached (%d per turn)", maxSearchCallsPerTurn)), nil
	}

	// Flush pipeline before search to ensure latest memories are available.
	if p.pipeline != nil {
		p.pipeline.Flush() //nolint:contextcheck // Flush uses context.Background internally by design
	}

	query := mcp.StringArg(arguments, "query")
	if query == "" {
		return mcp.BuildToolErrorResult("query is required"), nil
	}
	botID := strings.TrimSpace(session.BotID)
	if botID == "" {
		return mcp.BuildToolErrorResult("bot_id is required"), nil
	}
	chatID := strings.TrimSpace(session.ChatID)
	if chatID == "" {
		chatID = botID
	}

	limit := defaultMemoryToolLimit
	if value, ok, err := mcp.IntArg(arguments, "limit"); err != nil {
		return mcp.BuildToolErrorResult(err.Error()), nil
	} else if ok {
		limit = value
	}
	if limit <= 0 {
		limit = defaultMemoryToolLimit
	}
	if limit > maxMemoryToolLimit {
		limit = maxMemoryToolLimit
	}

	// Access control check.
	if chatID != botID {
		if p.chatAccessor == nil {
			return mcp.BuildToolErrorResult("chat service not available"), nil
		}
		chatObj, err := p.chatAccessor.Get(ctx, chatID)
		if err != nil {
			return mcp.BuildToolErrorResult("chat not found"), nil
		}
		if strings.TrimSpace(chatObj.BotID) != botID {
			return mcp.BuildToolErrorResult("bot mismatch"), nil
		}
		channelIdentityID := strings.TrimSpace(session.ChannelIdentityID)
		if channelIdentityID != "" {
			allowed, err := p.canAccessChat(ctx, chatID, channelIdentityID)
			if err != nil {
				return mcp.BuildToolErrorResult(err.Error()), nil
			}
			if !allowed {
				return mcp.BuildToolErrorResult("not a chat participant"), nil
			}
		}
	}

	// Determine search mode.
	mode := mcp.StringArg(arguments, "mode")
	if mode == "" {
		mode = "search"
	}

	switch mode {
	case "scene":
		return p.callToolSceneMode(ctx, botID, query, limit)
	case "time":
		afterStr := mcp.StringArg(arguments, "after")
		beforeStr := mcp.StringArg(arguments, "before")
		return p.callToolTimeMode(ctx, botID, query, limit, afterStr, beforeStr)
	default:
		return p.callToolSearchMode(ctx, botID, query, limit)
	}
}

// callToolSearchMode performs standard semantic search.
func (p *BuiltinProvider) callToolSearchMode(ctx context.Context, botID, query string, limit int) (map[string]any, error) {
	resp, err := p.service.Search(ctx, adapters.SearchRequest{
		Query: query,
		BotID: botID,
		Limit: limit,
		Filters: map[string]any{
			"namespace": sharedMemoryNamespace,
			"scopeId":   botID,
			"bot_id":    botID,
		},
		NoStats: true,
	})
	if err != nil {
		return mcp.BuildToolErrorResult("memory search failed"), nil
	}

	allResults := adapters.DeduplicateItems(resp.Results)
	sort.Slice(allResults, func(i, j int) bool {
		return allResults[i].Score > allResults[j].Score
	})
	if len(allResults) > limit {
		allResults = allResults[:limit]
	}

	results := make([]map[string]any, 0, len(allResults))
	for _, item := range allResults {
		results = append(results, map[string]any{
			"id":     item.ID,
			"memory": item.Memory,
			"score":  item.Score,
		})
	}

	return mcp.BuildToolSuccessResult(map[string]any{
		"query":   query,
		"mode":    "search",
		"total":   len(results),
		"results": results,
	}), nil
}

// callToolTimeMode performs time-range filtered search.
func (p *BuiltinProvider) callToolTimeMode(ctx context.Context, botID, query string, limit int, afterStr, beforeStr string) (map[string]any, error) {
	filters := map[string]any{
		"namespace": sharedMemoryNamespace,
		"scopeId":   botID,
		"bot_id":    botID,
	}

	// Parse time boundaries.
	if afterStr != "" {
		if t, err := time.Parse("2006-01-02", afterStr); err == nil {
			filters["created_at_gte"] = t.Format(time.RFC3339)
		}
	}
	if beforeStr != "" {
		if t, err := time.Parse("2006-01-02", beforeStr); err == nil {
			// Set to end of day.
			t = t.Add(24*time.Hour - time.Second)
			filters["created_at_lte"] = t.Format(time.RFC3339)
		}
	}

	resp, err := p.service.Search(ctx, adapters.SearchRequest{
		Query:   query,
		BotID:   botID,
		Limit:   limit,
		Filters: filters,
		NoStats: true,
	})
	if err != nil {
		return mcp.BuildToolErrorResult("memory time search failed"), nil
	}

	allResults := adapters.DeduplicateItems(resp.Results)
	sort.Slice(allResults, func(i, j int) bool {
		return allResults[i].Score > allResults[j].Score
	})
	if len(allResults) > limit {
		allResults = allResults[:limit]
	}

	results := make([]map[string]any, 0, len(allResults))
	for _, item := range allResults {
		results = append(results, map[string]any{
			"id":     item.ID,
			"memory": item.Memory,
			"score":  item.Score,
		})
	}

	return mcp.BuildToolSuccessResult(map[string]any{
		"query":   query,
		"mode":    "time",
		"after":   afterStr,
		"before":  beforeStr,
		"total":   len(results),
		"results": results,
	}), nil
}

// callToolSceneMode retrieves a scene and its associated memories.
func (p *BuiltinProvider) callToolSceneMode(ctx context.Context, botID, query string, limit int) (map[string]any, error) {
	if p.sceneStore == nil {
		// Fallback to regular search if scene store not configured.
		return p.callToolSearchMode(ctx, botID, query, limit)
	}

	// First, list scenes and find the best match by title/summary.
	scenes, err := p.sceneStore.List(ctx, botID)
	if err != nil || len(scenes) == 0 {
		// Fallback to regular search.
		return p.callToolSearchMode(ctx, botID, query, limit)
	}

	// Find the scene most relevant to the query (simple substring match).
	queryLower := strings.ToLower(query)
	var bestScene *scene.Scene
	bestScore := 0
	for i := range scenes {
		sc := &scenes[i]
		titleLower := strings.ToLower(sc.Title)
		summaryLower := strings.ToLower(sc.Summary)

		score := 0
		if strings.Contains(titleLower, queryLower) || strings.Contains(queryLower, titleLower) {
			score += 3
		}
		if strings.Contains(summaryLower, queryLower) {
			score += 2
		}
		// Check word overlap.
		queryWords := strings.Fields(queryLower)
		for _, w := range queryWords {
			if len(w) > 2 && (strings.Contains(titleLower, w) || strings.Contains(summaryLower, w)) {
				score++
			}
		}

		if score > bestScore {
			bestScore = score
			bestScene = sc
		}
	}

	if bestScene == nil || bestScore == 0 {
		// No matching scene found, fallback to regular search.
		return p.callToolSearchMode(ctx, botID, query, limit)
	}

	// Retrieve associated memories using the scene title as query.
	// Use a single search call with a higher limit instead of N individual calls.
	memLimit := limit
	if memLimit < len(bestScene.MemoryIDs) {
		memLimit = len(bestScene.MemoryIDs)
	}
	if memLimit > maxMemoryToolLimit {
		memLimit = maxMemoryToolLimit
	}

	resp, err := p.service.Search(ctx, adapters.SearchRequest{
		Query: bestScene.Title + " " + bestScene.Summary,
		BotID: botID,
		Limit: memLimit,
		Filters: map[string]any{
			"namespace": sharedMemoryNamespace,
			"scopeId":   botID,
			"bot_id":    botID,
		},
		NoStats: true,
	})
	if err != nil {
		return p.callToolSearchMode(ctx, botID, query, limit)
	}

	// Filter results to only include memories that belong to this scene.
	sceneMemIDs := make(map[string]bool, len(bestScene.MemoryIDs))
	for _, mid := range bestScene.MemoryIDs {
		sceneMemIDs[mid] = true
	}

	var sceneMemories []map[string]any
	for _, item := range resp.Results {
		if sceneMemIDs[item.ID] {
			sceneMemories = append(sceneMemories, map[string]any{
				"id":     item.ID,
				"memory": item.Memory,
			})
		}
		if len(sceneMemories) >= limit {
			break
		}
	}

	return mcp.BuildToolSuccessResult(map[string]any{
		"query": query,
		"mode":  "scene",
		"scene": map[string]any{
			"id":      bestScene.ID,
			"title":   bestScene.Title,
			"summary": bestScene.Summary,
		},
		"total":   len(sceneMemories),
		"results": sceneMemories,
	}), nil
}

func (p *BuiltinProvider) canAccessChat(ctx context.Context, chatID, channelIdentityID string) (bool, error) {
	if p.adminChecker != nil {
		isAdmin, err := p.adminChecker.IsAdmin(ctx, channelIdentityID)
		if err != nil {
			return false, err
		}
		if isAdmin {
			return true, nil
		}
	}
	if p.chatAccessor == nil {
		return false, errors.New("chat service not available")
	}
	return p.chatAccessor.IsParticipant(ctx, chatID, channelIdentityID)
}

// --- CRUD ---

func (p *BuiltinProvider) Add(ctx context.Context, req adapters.AddRequest) (adapters.SearchResponse, error) {
	if p.service == nil {
		return adapters.SearchResponse{}, errors.New("memory runtime not configured")
	}
	return p.service.Add(ctx, req)
}

func (p *BuiltinProvider) Search(ctx context.Context, req adapters.SearchRequest) (adapters.SearchResponse, error) {
	if p.service == nil {
		return adapters.SearchResponse{}, errors.New("memory runtime not configured")
	}
	return p.service.Search(ctx, req)
}

func (p *BuiltinProvider) GetAll(ctx context.Context, req adapters.GetAllRequest) (adapters.SearchResponse, error) {
	if p.service == nil {
		return adapters.SearchResponse{}, errors.New("memory runtime not configured")
	}
	return p.service.GetAll(ctx, req)
}

func (p *BuiltinProvider) Update(ctx context.Context, req adapters.UpdateRequest) (adapters.MemoryItem, error) {
	if p.service == nil {
		return adapters.MemoryItem{}, errors.New("memory runtime not configured")
	}
	return p.service.Update(ctx, req)
}

func (p *BuiltinProvider) Delete(ctx context.Context, memoryID string) (adapters.DeleteResponse, error) {
	if p.service == nil {
		return adapters.DeleteResponse{}, errors.New("memory runtime not configured")
	}
	return p.service.Delete(ctx, memoryID)
}

func (p *BuiltinProvider) DeleteBatch(ctx context.Context, memoryIDs []string) (adapters.DeleteResponse, error) {
	if p.service == nil {
		return adapters.DeleteResponse{}, errors.New("memory runtime not configured")
	}
	return p.service.DeleteBatch(ctx, memoryIDs)
}

func (p *BuiltinProvider) DeleteAll(ctx context.Context, req adapters.DeleteAllRequest) (adapters.DeleteResponse, error) {
	if p.service == nil {
		return adapters.DeleteResponse{}, errors.New("memory runtime not configured")
	}
	return p.service.DeleteAll(ctx, req)
}

func (p *BuiltinProvider) Compact(ctx context.Context, filters map[string]any, ratio float64, decayDays int) (adapters.CompactResult, error) {
	if p.service == nil {
		return adapters.CompactResult{}, errors.New("memory runtime not configured")
	}
	return p.service.Compact(ctx, filters, ratio, decayDays)
}

func (p *BuiltinProvider) Usage(ctx context.Context, filters map[string]any) (adapters.UsageResponse, error) {
	if p.service == nil {
		return adapters.UsageResponse{}, errors.New("memory runtime not configured")
	}
	return p.service.Usage(ctx, filters)
}

func (p *BuiltinProvider) Status(ctx context.Context, botID string) (adapters.MemoryStatusResponse, error) {
	if p.service == nil {
		return adapters.MemoryStatusResponse{}, errors.New("memory runtime not configured")
	}
	return p.service.Status(ctx, botID)
}

func (p *BuiltinProvider) Rebuild(ctx context.Context, botID string) (adapters.RebuildResult, error) {
	if p.service == nil {
		return adapters.RebuildResult{}, errors.New("memory runtime not configured")
	}
	return p.service.Rebuild(ctx, botID)
}
