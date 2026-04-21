# Architecture Quick Reference

## Service Topology

```
┌─────────────────────────────────────────────────┐
│                   Internet                      │
└──────┬──────────┬──────────┬───────────┬────────┘
       │          │          │           │
   Telegram    Discord    Misskey    Web UI
   WebSocket   Gateway    WebSocket  (browser)
       │          │          │           │
       └──────────┴──────────┘           │
                  │                      │
          ┌───────▼───────┐    ┌────────▼────────┐
          │  Server (Go)  │◄──►│  Web (Vue 3)    │
          │   :8080       │    │   :8082         │
          └───┬───┬───┬───┘    └─────────────────┘
              │   │   │
    ┌─────────┘   │   └────────────┐
    ▼             ▼                ▼
┌────────┐  ┌──────────┐  ┌──────────────┐
│Postgres│  │ Qdrant   │  │Browser GW    │
│ :5432  │  │ :6333    │  │ (Bun) :8083  │
└────────┘  └──────────┘  └──────────────┘
```

- **Server**: Go + Echo HTTP framework, in-process AI agent, all backend logic
- **Web**: Vue 3 + Vite, management UI
- **Browser Gateway**: Bun + Elysia + Playwright, headless browser automation

## Request Lifecycle

A message from a user on any platform follows this path:

```
Channel Adapter (telegram/discord/misskey/...)
  │  Receives platform-specific event
  │  Normalizes to InboundMessage
  ▼
Channel Service (internal/channel/)
  │  ACL check, session routing
  │  Persists inbound message
  ▼
Conversation Flow Resolver OR Pipeline
  │  Two orchestration paths:
  │
  ├─ Flow Resolver (internal/conversation/flow/)
  │    Chat mode: direct agent invocation
  │    resolver.go → resolver_stream.go
  │
  └─ Pipeline DCP (internal/pipeline/)
       Discuss mode: multi-user group chat
       driver.go orchestrates timing
  ▼
Agent (internal/agent/agent.go)
  │  Assembles prompt (system + history + tools)
  │  Calls LLM via Twilight AI SDK
  │  Streams SSE events back
  ▼
Tool Execution (internal/agent/tools/*.go)
  │  Agent may call tools mid-stream
  ▼
Outbound Message (internal/messaging/)
  │  Sends response back through channel adapter
  ▼
Channel Adapter
     Platform-specific delivery
```

## Pipeline DCP Flow (Discuss Mode)

The Deterministic Context Pipeline processes group chat messages in 4 stages:

```
InboundMessage
  │
  ▼ adapt.go: AdaptInbound()
CanonicalEvent (MessageEvent/EditEvent/DeleteEvent/ServiceEvent)
  │
  ▼ projection.go: Reduce()
IntermediateContext (IC) — ordered ICNode entries + per-user state
  │  Pure function — never mutates input
  ▼ rendering.go: Render()
RenderedContext (RC) — []RenderedSegment with flags:
  │  is_myself, mentions_me, replies_to_me, is_timeline
  ▼ context.go: ComposeContext()
[]ContextMessage — final prompt input for LLM
  │
  ▼ driver.go: DiscussDriver
Agent.Stream() or Generate()
```

**DiscussDriver timing flow:**
```
Message arrives → Debounce (quiet period)
  → Check talk_value threshold
  → Timing Gate (optional LLM probe: continue/wait/no_reply)
  → wasRecentlyMentioned? (@mention/timeline → skip timing)
  → Agent.Stream()
  → Response delivered
```

## Agent Architecture

### Core (internal/agent/)

| File | Responsibility |
|------|---------------|
| `agent.go` | `Stream()` (SSE) and `Generate()` (non-streaming), retry loop, watchdog |
| `stream.go` | Streaming event types, `StreamEvent` struct with retry fields |
| `prompt.go` | Prompt assembly from templates |
| `retry.go` | `RetryConfig`, `isRetryableStreamError()`, exponential backoff |
| `guard_state.go` | Guard state management |
| `sential.go` | Sentinel/loop detection logic |

### Tool System (internal/agent/tools/)

```go
type ToolProvider interface {
    Tools(ctx context.Context, session SessionContext) ([]sdk.Tool, error)
}
```

Each tool provider returns `[]sdk.Tool` with `Name`, `Description`, JSON Schema `Parameters`, and an `Execute` closure. Registered via setter injection to avoid FX dependency cycles.

### Prompt Templates (internal/agent/prompts/)

- **System prompts**: `system_chat.md`, `system_discuss.md`, `system_heartbeat.md`, `system_schedule.md`, `system_subagent.md`
- **Partials** (prefixed `_`): `_tools.md`, `_memory.md`, `_contacts.md`, `_schedule_task.md`, `_subagent.md`
- **Operational**: `heartbeat.md`, `schedule.md`, `memory_extract.md`, `memory_update.md`
- Syntax: Custom regex-based `{{include:_partial_name}}` replacement (not Go text/template)

## Channel Adapter System

```go
// Base interface
type Adapter interface {
    Type() ChannelType
    Descriptor() Descriptor
}

// 12 optional capability interfaces (implement as needed):
Sender, Receiver, StreamSender, MessageEditor, Reactor,
ConfigNormalizer, TargetResolver, BindingMatcher,
WebhookReceiver, SelfDiscoverer, AttachmentResolver,
ProcessingStatusNotifier
```

Registry dispatches via runtime type assertion: `adapter.(Sender)`, `adapter.(Receiver)`, etc.

## Uber FX Dependency Injection

Entry point: `cmd/agent/main.go` (standalone server with FX wiring).

Key pattern:
- Services are Go structs with constructor functions
- FX modules group related providers
- Circular dependencies resolved via setter injection (e.g., tool providers)

## Key Directory Index

| Path | What to find here |
|------|------------------|
| `internal/agent/` | AI agent core, tools, prompts |
| `internal/channel/` | Channel adapter framework + adapters |
| `internal/conversation/flow/` | Chat mode resolver |
| `internal/pipeline/` | Discuss mode pipeline (DCP) |
| `internal/chattiming/` | Smart timing (debounce, talk_value, timing gate) |
| `internal/memory/` | Long-term memory (Qdrant, BM25, LLM extraction) |
| `internal/compaction/` | Context compaction service |
| `internal/tts/` | Text-to-speech framework + adapters |
| `internal/workspace/` | Container lifecycle management |
| `internal/handlers/` | HTTP REST API handlers |
| `internal/db/sqlc/` | Auto-generated DB code (DO NOT EDIT) |
| `db/queries/` | sqlc query definitions |
| `db/migrations/` | SQL migrations |
| `conf/providers/` | LLM provider YAML templates |
| `apps/web/` | Vue 3 frontend |
| `apps/browser/` | Browser automation gateway |

## Timeout Architecture

Adaptive idle timeout (not wall-clock):

- Default base timeout: **90s** — resets on every SSE event via `Reset()`
- Adaptive extension: **+60s per tool call**, capped at **600s** total
- Tool calls (spawn/subagent) can take minutes, so the timeout extends generously

Implementation: `internal/conversation/flow/idle_timeout.go` uses `time.AfterFunc` with `idleCancel` struct. `currentTimeout()` returns `base + 60s * toolCalls`, capped at 600s.

## Retry Architecture

| Layer | Scope | Policy |
|-------|-------|--------|
| Agent (`agent.go`) | LLM stream call | 10 attempts: 5 fast + 5 exponential backoff |
