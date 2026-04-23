# Problem Report: Discuss Mode Repeated Message Sending (Misskey Timeline)

**Date:** Apr 23, 2026
**Status:** Open — Root Causes Identified, Fixes Not Yet Implemented
**Severity:** Critical — Causes spam on external platforms (297 duplicate notes on Misskey)
**Affected Platforms:** Misskey (primary), potentially all platforms using discuss mode with timeline
**Related Incident:** [postmortem-image-gen-outbound-spam.md](./postmortem-image-gen-outbound-spam.md)

---

## Summary

The bot in discuss mode sends duplicate or repeated messages to the Misskey timeline. This is a **recurring problem** — despite a previous fix (commit `08af90fd`, which addressed `emitImageAttachment` spam), the issue has resurfaced. This indicates the previous fix addressed one symptom but not the root cause. The problem is systemic: **discuss mode's architecture has multiple failure modes that can cause repeated sending**, and no single fix resolves all of them.

---

## Root Cause Analysis

After a thorough code review of the entire discuss mode pipeline — from inbound message reception through the DiscussDriver to the agent's `send` tool execution — I identified **five distinct failure modes** that can cause repeated message sending.

### Root Cause 1: DiscussDriver Repeatedly Triggers Agent on Active Timeline (CRITICAL)

**The most likely cause of the current repeated sending.**

The DiscussDriver's `runSession()` loop is driven by incoming RC (RenderedContext) pushes via `NotifyRC()`. Every time a new message arrives on the timeline, a new RC is pushed, which can trigger another agent call. In an active timeline with multiple users posting, the agent may be invoked **many times in quick succession**, and each invocation tends to call the `send` tool because:

1. The `buildLateBindingPrompt()` appends a user message saying "You MUST use the `send` tool to speak" every time the agent is invoked (`driver.go:467-468`)
2. When `isMentioned` is true, it adds: "You were mentioned or replied to. You should respond by calling the `send` tool now." — creating an even stronger push to respond
3. The smart timing checks (talk_value threshold, timing gate) are the only gates preventing repeated invocation, but these may not be configured or may have low thresholds
4. **Each agent invocation is independent** — there is no memory between invocations about what was already sent. The agent cannot know it just sent an identical message 5 seconds ago

**Why this is the most likely cause:** The cleanup script deleted 297 notes that were all from the bot itself. This volume indicates rapid repeated agent invocations, each producing a new note. The notes likely had similar but not identical content (rephrased by the LLM each time), which explains why the `ToolLoopGuard` (which detects identical tool inputs) did not catch them.

**Feedback loop aggravation:** When the bot posts a public note (`visibility: "public"`) or home note (`visibility: "home"`), it appears on the timeline. Other users may react/reply to these notes, generating more timeline activity, which triggers more agent calls. Even though the self-filter (`note.UserID == me.ID`) correctly blocks the bot's own notes, the **conversational ripple effect** (other users reacting to bot's spam) creates a positive feedback loop.

**Evidence:**
- `driver.go:467-468`: Late binding prompt appended as user message every invocation
- `driver.go:807-822`: `buildLateBindingPrompt()` with strong "MUST call send" language
- `driver.go:307-337`: Talk value threshold check — only gate before agent call
- `driver.go:340-389`: Timing gate — optional, may not be configured
- `driver.go:391`: `d.handleReplyWithAgent()` called on every invocation that passes the gates
- `misskey.go:788-792`: Bot notes use `visibility: "public"` (standalone) or `"home"` (replies) — both appear on timeline
- `misskey.go:614`: Self-filter works correctly (`note.UserID == me.ID`), but does not prevent OTHER users' reactions from triggering new agent calls

**Code locations:**
- `internal/pipeline/driver.go:239-305` — `runSession()` loop
- `internal/pipeline/driver.go:307-392` — `handleReply()` with timing gates
- `internal/pipeline/driver.go:394-538` — `handleReplyWithAgent()` agent invocation
- `internal/pipeline/driver.go:807-822` — `buildLateBindingPrompt()`
- `internal/channel/adapters/misskey/misskey.go:788-792` — note visibility

### Root Cause 2: LLM Tool-Call Loop in Discuss Mode (HIGH)

The discuss mode prompt instructs the LLM:

> "Your text output is invisible to everyone... you MUST call the `send` tool."

And:

> "On every turn where you make tool calls, also include a `send` call briefly explaining what you are doing."

This creates a strong bias toward calling `send`. When combined with:

1. **Tool loop detection is insufficient for discuss mode**: The `ToolLoopGuard` (`internal/agent/sential.go`) detects when the same tool is called with the **identical** arguments repeatedly (SHA-256 hash of normalized input). But across different DiscussDriver invocations, the LLM generates different context each time (new timestamps, new conversation history), so the `send` tool input differs even if the semantic content is similar. Within a single agent call, the LLM may also call `send` with slightly different text each time (e.g., rephrasing), evading the loop detector.

2. **No per-turn send count limit**: There is no mechanism to limit the number of `send` tool calls per agent turn. The LLM can call `send` multiple times in a single response — the prompt explicitly encourages parallel tool calls. Each call to `send` in discuss mode goes through `SendDirect()` → `MisskeyAdapter.Send()` → `createNote()`, creating a separate note on the platform.

3. **No cross-invocation dedup**: Each DiscussDriver agent invocation is independent. There is no check whether a message with similar content was already sent in a previous invocation seconds ago.

**Code locations:**
- `internal/agent/prompts/system_discuss.md:1,43` — "MUST call send" emphasis
- `internal/agent/sential.go:507` — `ToolLoopGuard.Guard()` — tool loop detection
- `internal/agent/agent.go:104-118` — loop detection setup

### Root Cause 3: Discuss Driver Retries on Interrupt (MEDIUM)

The `DiscussDriver.handleReplyWithAgent()` has an interrupt-retry loop:

```go
maxRounds := 7 // loop bound; CanRetry() caps at 1 initial + 5 retries
for round := 0; round < maxRounds; round++ {
    // ... run agent ...
    if wasInterrupted && sess.interrupt.CanRetry() {
        continue // Retry with fresh Bind()
    }
}
```

Each retry round runs a fresh agent call. If the agent calls `send` in round N, and then gets interrupted, round N+1 may call `send` again with similar content. The `lastProcessedMs` is only updated after the final successful round, so the retry sees the same inbound context.

**Code location:** `internal/pipeline/driver.go:394-538`

### Root Cause 4: Pipeline Event Re-Ingestion After Reconnect (MEDIUM)

When the Misskey WebSocket reconnects (every disconnection triggers `runStreamLoop` → new `runStream`), the pipeline's `IC` (In-memory Conversation) state may not be properly reset:

```go
if _, loaded := p.pipeline.GetIC(sessionID); !loaded {
    p.replayPipelineSession(ctx, sessionID)
}
```

If the session IC was garbage-collected or evicted between reconnects, `replayPipelineSession` replays all persisted events. This replay can produce a `RenderedContext` that includes old messages, which the DiscussDriver may treat as new (depending on `lastProcessedMs` state, which is per-session and lost if the discuss session was stopped and restarted).

**Code location:** `internal/channel/inbound/channel.go:432-435`

### Root Cause 5: No Outbound Deduplication at the Channel Layer (LOW)

There is no mechanism at the channel adapter level to prevent sending duplicate notes. The Misskey adapter's `Send()` method creates a note without checking if an identical note was recently created. While the chat mode has `isMessagingToolDuplicate()` (which compares outgoing text against previously sent tool calls), this check:

1. Only works within a single stream invocation
2. Does not persist across retries or new agent turns
3. Is not applied in discuss mode (discuss mode uses `SendDirect`, bypassing the normal stream pipeline)

**Code locations:**
- `internal/channel/inbound/channel.go:1605-1623` — `isMessagingToolDuplicate()` (chat mode only)
- `internal/agent/tools/message.go:92-104` — discuss mode `SendDirect` bypass

---

## Architecture Diagram: Discuss Mode Message Flow

```
Misskey WebSocket
    │
    ▼
MisskeyAdapter.handleTimelineNote()     ← Self-filter: note.UserID == me.ID
    │
    ▼
ChannelInboundProcessor.HandleInbound()
    │
    ├── PushEvent → Pipeline → RenderedContext
    │
    ▼
DiscussDriver.NotifyRC()
    │
    ▼
DiscussDriver.runSession() loop
    │
    ├── Debounce Wait
    ├── Drain additional RCs
    ├── wasRecentlyMentioned() check
    ├── Talk value threshold
    ├── Timing Gate (optional LLM probe)
    │
    ▼
DiscussDriver.handleReplyWithAgent()
    │
    ├── Resolve RunConfig
    ├── Agent.Stream() ← LLM call
    │     │
    │     ├── LLM calls "send" tool
    │     │     │
    │     │     ▼
    │     │   MessageProvider.execSend()
    │     │     │
    │     │     ├── session.SessionType == "discuss" ?
    │     │     │     └── YES → SendDirect() → channel.Manager.Send()
    │     │     │                          → MisskeyAdapter.Send()
    │     │     │                          → createNote() API    ← NOTE APPEARS ON TIMELINE
    │     │     │
    │     │     └── NO → Local shortcut (emit to stream)
    │     │
    │     └── Stream events: EventAgentEnd / EventAgentAbort
    │
    ├── Interrupt retry? → loop back (up to 7 rounds)
    │
    └── StoreRound → persist to bot_history_messages
```

**Feedback loop path:**
```
Any timeline message → NotifyRC → handleReply → handleReplyWithAgent
  → Agent calls send() → createNote() → Note appears on Misskey
  → Other users react/reply → New timeline messages → NotifyRC → ...
```

**Note:** The bot's own notes are correctly filtered (`note.UserID == me.ID`), but the
conversational activity generated by the bot's notes (replies from others, reactions)
creates additional timeline events that re-trigger the DiscussDriver.

---

## Why the Previous Fix Was Insufficient

The previous fix (commit `08af90fd`) addressed a specific case where `emitImageAttachment` caused spam in discuss mode. That fix:

1. Skipped `emitImageAttachment` for discuss mode sessions
2. Prevented the image-specific tool loop

**But it did NOT address:**
- The fundamental issue of DiscussDriver repeatedly invoking the agent on active timelines without cross-invocation dedup
- The lack of outbound deduplication at the channel layer
- The LLM's strong bias toward calling `send` due to prompt instructions and late binding
- The lack of per-turn send count limits
- The interrupt-retry loop in the DiscussDriver creating additional send opportunities

---

## Recommended Fixes

### Fix 1: Outbound Deduplication at Channel Layer (Addresses Root Cause 1 & 5)

Add a short-TTL dedup cache at the channel adapter level (or in the channel Manager) that tracks recently sent messages. Before sending, check if an identical or very similar message was sent to the same target within the last N seconds.

**Implementation:**
```go
// In MisskeyAdapter or channel.Manager
type outboundDedup struct {
    mu    sync.Mutex
    cache map[string]time.Time // key: hash(platform+target+text) → sent time
    ttl   time.Duration        // e.g., 30 seconds
}
```

**Location:** `internal/channel/adapters/misskey/misskey.go` or `internal/channel/manager.go`

### Fix 2: Cross-Invocation Dedup in DiscussDriver (Addresses Root Cause 1)

1. **Track recently sent messages**: Maintain a short-lived cache (e.g., 60s TTL) of sent message content hashes in the `discussSession` struct. Before the agent's `send` tool executes, check if a similar message was recently sent.
2. **Minimum interval between agent calls**: Add a minimum cooldown (e.g., 30s) between consecutive agent invocations for the same session, regardless of how many RCs arrive.
3. **Log invocation frequency**: Track and log how often the agent is invoked per session to detect runaway loops early.

**Location:** `internal/pipeline/driver.go:100-115` (add to `discussSession` struct) and `internal/pipeline/driver.go:307` (add cooldown in `handleReply`)

### Fix 3: Per-Turn Send Limit in Discuss Mode (Addresses Root Cause 2)

Add a counter in the `SessionContext` or discuss session state that limits the number of `send` tool calls per agent turn. After N sends (e.g., 3), subsequent `send` calls return an error.

**Location:** `internal/agent/tools/message.go:85-131`

### Fix 4: Dedup Persisted Messages Across Retries (Addresses Root Cause 2 & 3)

When the DiscussDriver retries after an interrupt, check if a message with similar content was already stored in the current turn. If so, skip the retry or suppress the duplicate.

**Location:** `internal/pipeline/driver.go:394-538`

### Fix 5: Harden Prompt to Reduce Unnecessary Sends (Addresses Root Cause 2)

Revise the discuss mode system prompt to:
- Remove the strong "MUST call send" emphasis
- Add explicit instruction: "Call `send` at most once per turn unless you have distinct information to convey in each call"
- Add: "If you already called `send` in this turn, do not call it again with similar content"

**Location:** `internal/agent/prompts/system_discuss.md`

### Fix 6: Discuss Driver Outbound Tracking (Addresses Root Cause 1 — Defense in Depth)

Track outbound message IDs in the DiscussDriver session state. When a new RenderedContext arrives, check if any of its segments correspond to the bot's own recently-sent messages, and skip them.

**Location:** `internal/pipeline/driver.go:100-115` (add to `discussSession` struct)

---

## Fix Priority Matrix

| Fix | Addresses | Effort | Impact | Priority |
|-----|-----------|--------|--------|----------|
| Fix 1: Outbound dedup at channel | RC 1, 5 | Small | High | **P0** |
| Fix 2: Cross-invocation dedup | RC 1 | Medium | High | **P0** |
| Fix 3: Per-turn send limit | RC 2 | Small | High | **P0** |
| Fix 4: Dedup across retries | RC 2, 3 | Medium | Medium | **P1** |
| Fix 5: Harden prompt | RC 2 | Small | Medium | **P1** |
| Fix 6: Outbound tracking | RC 1 | Medium | Medium | **P2** |

---

## Investigation Notes

### What Was Checked
- Full Misskey adapter code (4 files, ~1400 lines)
- DiscussDriver and pipeline code (`internal/pipeline/driver.go`, ~838 lines)
- Channel inbound processor (`internal/channel/inbound/channel.go`, ~2000 lines)
- Agent core loop and tool execution (`internal/agent/agent.go`, `internal/agent/tools/message.go`)
- Message persistence and event hub (`internal/message/`)
- Heartbeat and schedule systems (`internal/heartbeat/`, `internal/schedule/`)
- Sential (loop detection) system (`internal/agent/sential.go`)
- Discuss mode system prompt (`internal/agent/prompts/system_discuss.md`)

### Key Files Reference

| File | Relevance |
|------|-----------|
| `internal/channel/adapters/misskey/misskey.go` | Misskey adapter: WebSocket, inbound filtering, outbound send |
| `internal/pipeline/driver.go` | DiscussDriver: session management, agent triggering, interrupt retry |
| `internal/channel/inbound/channel.go` | Inbound processing: discuss routing, pipeline push, NotifyRC |
| `internal/agent/tools/message.go` | Send tool: discuss mode SendDirect path |
| `internal/messaging/executor.go` | Message executor: Send vs SendDirect |
| `internal/agent/prompts/system_discuss.md` | Discuss mode system prompt |
| `internal/agent/sential.go` | Loop detection (text + tool) |
| `internal/agent/agent.go` | Agent core: stream loop, tool assembly |
