package pipeline

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	agentpkg "github.com/memohai/memoh/internal/agent"
	"github.com/memohai/memoh/internal/channel"
	"github.com/memohai/memoh/internal/chattiming"
	"github.com/memohai/memoh/internal/conversation"
	"github.com/memohai/memoh/internal/expression"
	"github.com/memohai/memoh/internal/memory/adapters"
)

// ResolveRunConfigResult holds the output of ResolveRunConfig.
type ResolveRunConfigResult struct {
	RunConfig agentpkg.RunConfig
	ModelID   string // database UUID of the selected model
}

// RunConfigResolver resolves a complete agent RunConfig for the timing gate
// probe. The main agent call now goes through DiscussChatRunner.
type RunConfigResolver interface {
	ResolveRunConfig(ctx context.Context, botID, sessionID, channelIdentityID, currentPlatform, replyTarget, conversationType, chatToken string) (ResolveRunConfigResult, error)
}

// DiscussChatRunner executes a full chat round (resolve → agent → store) for
// discuss mode. Implemented by flow.Resolver which provides StreamChat.
type DiscussChatRunner interface {
	StreamChat(ctx context.Context, req conversation.ChatRequest) (<-chan conversation.StreamChunk, <-chan error)
}

// DiscussStreamBroadcaster publishes stream events to local UI subscribers.
// Implemented by local.RouteHub.
type DiscussStreamBroadcaster interface {
	PublishEvent(routeKey string, event channel.StreamEvent)
}

// DiscussChannelSender provides outbound stream capabilities for discuss mode.
// Implemented by channel.Manager.
type DiscussChannelSender interface {
	GetReplySender(botID string, channelType channel.ChannelType) (channel.StreamReplySender, error)
}

// DiscussReactor sends emoji reactions to messages on the chat platform.
// Implemented by channel.Manager.
type DiscussReactor interface {
	React(ctx context.Context, botID string, channelType channel.ChannelType, req channel.ReactRequest) error
}

// DiscussDispatcher abstracts the RouteDispatcher for discuss mode, providing
// inject/queue capabilities without importing the inbound package.
type DiscussDispatcher interface {
	IsActive(routeID string) bool
	MarkActive(routeID string) <-chan conversation.InjectMessage
	TryMarkActive(routeID string) <-chan conversation.InjectMessage
	MarkDone(routeID string) DiscussMarkDoneResult
	Inject(routeID string, msg conversation.InjectMessage) bool
}

// DiscussMarkDoneResult holds the data returned when a discuss route transitions
// from active to idle.
type DiscussMarkDoneResult struct {
	QueuedNotifications []DiscussQueuedNotification
}

// DiscussQueuedNotification holds a queued RC notification that arrived while
// the discuss agent was active. These are replayed after the agent completes.
type DiscussQueuedNotification struct {
	SessionID string
	RC        RenderedContext
	Config    DiscussSessionConfig
}

// DiscussStreamChunkParser parses raw agent stream chunks into channel events.
// Injected to avoid import cycles between pipeline and channel/inbound.
type DiscussStreamChunkParser func(chunk conversation.StreamChunk) ([]channel.StreamEvent, []conversation.ModelMessage, error)

// DiscussAssistantOutputExtractor extracts visible assistant text from model messages.
// Injected to avoid import cycles between pipeline and conversation/flow.
type DiscussAssistantOutputExtractor func(messages []conversation.ModelMessage) []conversation.AssistantOutput

// DiscussTriggerDeps holds dependencies injected into the DiscussTrigger.
type DiscussTriggerDeps struct {
	Pipeline    *Pipeline
	EventStore  *EventStore
	Resolver    RunConfigResolver
	Broadcaster DiscussStreamBroadcaster
	Logger      *slog.Logger

	ChatRunner               DiscussChatRunner
	ChannelSender            DiscussChannelSender
	Reactor                  DiscussReactor
	Dispatcher               DiscussDispatcher
	StreamObserver           channel.StreamObserver
	StreamChunkParser        DiscussStreamChunkParser
	AssistantOutputExtractor DiscussAssistantOutputExtractor
	ChatTimingService        *chattiming.Service
	SettingsService          settingsLoader
	MemoryFormation          memoryFormationRunner
	ExpressionAccumulator    ExpressionAccumulator // optional: expression/jargon learning
	ExpressionSelector       *expression.Selector  // optional: expression style selection for replyer
}

// memoryFormationRunner runs the Extract -> Decide -> Apply pipeline on messages.
type memoryFormationRunner interface {
	OnAfterChat(ctx context.Context, req adapters.AfterChatRequest) error
}

// settingsLoader is a minimal interface for loading bot settings.
type settingsLoader interface {
	GetBotChatTiming(ctx context.Context, botID string) (json.RawMessage, error)
}

// ExpressionAccumulator accumulates chat messages for offline expression/jargon learning.
// Called from extractPassiveMemory when the bot decides not to reply.
type ExpressionAccumulator func(ctx context.Context, botID, sessionID string, messages []adapters.Message)

// ExpressionMessage is a minimal message representation for expression learning.
type ExpressionMessage struct {
	Role    string
	Content string
}

// DiscussSessionConfig holds per-session configuration for discuss mode.
type DiscussSessionConfig struct {
	BotID             string
	SessionID         string
	ChannelIdentityID string
	ReplyTarget       string
	CurrentPlatform   string
	ConversationType  string
	ConversationName  string
	SessionToken      string //nolint:gosec // session credential material
	RouteID           string // dispatcher route key (typically botID:conversationID)
}

// discussSession holds the runtime state for a single discuss-mode session goroutine.
type discussSession struct {
	config          DiscussSessionConfig
	rcCh            chan RenderedContext
	stopCh          chan struct{}
	cancel          context.CancelFunc
	lastProcessedMs int64
	lastAgentCallAt time.Time

	// Smart timing state (nil when feature disabled).
	chatTimingCfg    *chattiming.Config
	debounce         *chattiming.Debouncer
	interrupt        *chattiming.InterruptController
	timingGate       *chattiming.TimingGate
	idleCompensate   *chattiming.IdleCompensator
	cachedProbeModel *agentpkg.RunConfig
	msgIntervals     []time.Duration
}
