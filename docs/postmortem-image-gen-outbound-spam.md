# Postmortem: image_gen Outbound Spam (Apr 22, 2026)

## Summary

A bug in the `emitImageAttachment` feature caused the Misskey bot to send **1,130 outbound note-creation requests** in under 50 seconds, triggering Misskey's rate limit and forcing an emergency API key revocation. The server remained running but the Misskey channel became inoperable.

## Timeline

| Time (UTC) | Event |
|------------|-------|
| 09:26 | Commit `69caa51e` merged: added `emitImageAttachment` to `image_gen` tool |
| ~14:56 | Docker image rebuilt with correct tag (`memohai/server:latest`) and deployed |
| 15:02 | User asks bot to send an image test on Misskey timeline |
| 15:02:16 | First normal outbound (text response) sent successfully |
| 15:02:51 | Bot calls `generate_image` → `emitImageAttachment` fires → **outbound spam begins** |
| ~15:04 | 1,130 `send outbound` calls logged in ~2 seconds |
| ~15:05 | Misskey API returns 401 (key revoked by instance admin) |
| ~15:05 | Server continues retrying with exponential backoff |
| 15:10 | User reports bug, server stopped |

## Root Cause

### Primary: `emitImageAttachment` incompatible with Discuss mode

The `emitImageAttachment` method emitted a `StreamEventAttachment` via `session.Emitter` regardless of session type. However, the **DiscussDriver does not handle `EventAttachment` events**:

1. `session.Emitter` is non-nil in discuss mode (created by `Agent.Stream()`)
2. The attachment event becomes `EventAttachment` ("attachment_delta") in the agent stream
3. The DiscussDriver's event loop (`driver.go:476-481`) only handles `EventError`, `EventAgentEnd`, `EventAgentAbort` — **no case for `EventAttachment`**
4. `broadcastDiscussEvent` → `agentEventToChannelEvent` also has **no mapping for `EventAttachment`** (falls to `default: return false`)
5. The attachment event is silently discarded

The LLM then called `send` with the image attachment, but since it saw no confirmation of delivery, it entered a **tool-calling loop** — repeatedly calling `send` trying to deliver the image.

### Secondary: Docker build cache served stale binary

The first build attempt used `docker build -t memoh-server:latest` but `docker-compose.yml` references `memohai/server:latest`. The Docker build cache (`--mount=type=cache` for Go build cache) also served stale compiled packages. Required `--no-cache` build with the correct image tag.

### Contributing: Misskey API key not rate-limited

The Misskey adapter has retry logic with exponential backoff but no circuit breaker. When the API started returning 401 (after key revocation), the server continued retrying.

## Event Flow (Buggy Path)

```
LLM calls generate_image("a cat")
  → image_gen returns {"path": "/data/generated-images/xxx.png", ...}
  → emitImageAttachment fires StreamEventAttachment via session.Emitter
    → Agent stream produces EventAttachment ("attachment_delta")
      → DiscussDriver: falls through switch (no handler) ← BUG HERE
      → broadcastDiscussEvent: agentEventToChannelEvent returns false ← ALSO DROPPED
    → Attachment silently discarded
  → LLM sees tool result but no image delivery confirmation
  → LLM calls send(attachments=["/data/generated-images/xxx.png"])
    → Discuss mode → SendDirect → Sender.Send() → Misskey API
    → Repeat... 1130 times
```

## Fix

**Commit `08af90fd`**: Skip `emitImageAttachment` when `session.SessionType == "discuss"`.

```go
if session.SessionType != "discuss" {
    p.emitImageAttachment(session, result)
}
```

In discuss mode, the LLM must use the `send` tool explicitly to deliver images. In chat mode, `emitImageAttachment` works correctly because the `ChannelInboundProcessor` handles `attachment_delta` events and forwards them to the outbound stream.

## Action Items

| Item | Status | Priority |
|------|--------|----------|
| Skip emitAttachment in discuss mode | Done (`08af90fd`) | Critical |
| Update Misskey API access token | Pending (user action) | Critical |
| Add EventAttachment handling to DiscussDriver | Not started | High |
| Add circuit breaker for outbound retries | Not started | Medium |
| Add tool-loop detection for discuss mode | Not started | Medium |

## Lessons Learned

1. **Test across all session types**: Features that use `session.Emitter` must be verified in both chat and discuss modes, since the DiscussDriver has a different event processing pipeline.
2. **Docker image tags matter**: Always verify the image tag in `docker-compose.yml` matches the `docker build -t` tag.
3. **LLM tool loops can be expensive**: When an LLM tool doesn't produce the expected side effect, the LLM may enter a retry loop. Loop detection thresholds should apply to discuss mode.
