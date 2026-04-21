# Troubleshooting Guide

## Quick Log Commands

```bash
# Server logs (last 50 lines, last 10 minutes)
docker logs memoh-server --tail 50 --since 10m

# Follow server logs in real-time
docker logs memoh-server -f --since 5m

# Filter for errors only
docker compose logs server --since 10m 2>&1 | grep -iE "ERROR|error|panic|fatal"

# Filter for a specific bot ID
docker compose logs server --since 10m 2>&1 | grep "BOT_ID_HERE"

# Check container status
docker compose ps
```

---

## Bot Not Responding

### Step 1: Check if server is running
```bash
docker compose ps
curl -s http://localhost:8080/health
```

### Step 2: Check server logs for errors
```bash
docker logs memoh-server --tail 100 --since 5m | grep -iE "error|timeout|fail"
```

### Common Causes

| Symptom | Likely Cause | Fix |
|---------|-------------|-----|
| No log entries for bot | Channel adapter disconnected | Restart server, check channel config |
| `429 Too Many Requests` | LLM provider rate limit | Wait, or switch to different model/provider |
| `context deadline exceeded` | Stream timeout (see below) | Check idle timeout configuration |
| `Unprocessable Entity` | API compatibility issue | Check client_type matches the provider API |
| Agent starts but no response | Context too large, LLM can't process | Check compaction (see below) |
| `net/http: timeout awaiting response headers` | LLM provider slow to respond | Increase timeout or add retry |

---

## Stream Timeout / Bot Interrupted

### Architecture

The system uses **adaptive idle timeout** (not wall-clock timeout):
- Resets on every SSE event received from LLM
- Default base: **90s**, extends +60s per tool call, capped at **600s**
- Only fires when truly idle (no events for the computed timeout)

### Diagnosis

```bash
# Look for timeout-related log entries
docker logs memoh-server --tail 200 --since 10m | grep -i "idle\|timeout\|cancel"
```

### Common Patterns

**Multi-tool calls timing out**: Agent makes several tool calls in sequence (e.g., read file → edit → run command). Each tool call generates events, so idle timeout should NOT fire. If it does, check that the tool is emitting progress events.

**Provider accepts connection but never streams**: The per-event 60s idle timeout catches this. The provider returned HTTP 200 but never sent any SSE data.

**Rate limit causing timeout**: Some providers (GLM/Zhipu) return 401 for rate limiting instead of 429. The retry layer handles this, but if all retries exhaust, the stream fails.

### Key Files
- `internal/conversation/flow/idle_timeout.go` — Idle timeout implementation
- `internal/conversation/flow/resolver_stream.go` — Stream event loop with idle reset
- `internal/agent/retry.go` — Retry with backoff

---

## Context Compaction Deadlock

### Symptom
Bot works for a while, then stops responding to messages. Logs show the agent receiving messages but LLM calls fail repeatedly.

### Root Cause
Compaction was designed to trigger only **after** a successful LLM response. When context grows too large for the LLM to ever respond successfully, compaction never triggers → deadlock cycle.

### Fix (Already Applied)
- Compaction now triggers **before** calling LLM when estimated tokens exceed 70% of model context window
- Messages exceeding compression model's context are truncated to 90% before sending
- After compression: keep latest 3 messages + summary (not full history)
- Tool results are stripped from retained messages

### Diagnosis
```bash
# Check if compaction is triggering
docker logs memoh-server --tail 200 | grep -i "compact\|truncat"

# Check message count for a session
docker compose exec postgres psql -U memoh -d memoh -c \
  "SELECT COUNT(*) FROM bot_history_messages WHERE session_id = 'SESSION_ID'"
```

### Key Files
- `internal/conversation/flow/resolver_compaction.go` — Compaction triggering
- `internal/compaction/service.go` — Compaction service
- `internal/conversation/flow/resolver_stream.go` — Proactive compaction before LLM call

---

## LLM API Error Reference

| HTTP Status | Meaning | Common Cause | Action |
|-------------|---------|-------------|--------|
| 400 | Bad Request | Malformed request body | Check client_type matches API format |
| 401 | Unauthorized | Invalid API key OR rate limit (GLM) | Check key; GLM uses 401 for rate limiting |
| 429 | Rate Limited | Too many requests | System auto-retries; wait or switch provider |
| 422 | Unprocessable Entity | API incompatibility | Check client_type (e.g., xAI needs Responses API) |
| 500 | Server Error | Provider issue | Auto-retry; check provider status |
| 502/503 | Gateway Error | Provider overloaded | Auto-retry with backoff |

### xAI Specific Issues

xAI uses `/v1/responses` instead of `/v1/chat/completions`. Parameter mapping:
- `messages` → `input`
- `max_tokens` → `max_output_tokens`

If you see `Unprocessable Entity` with xAI, check the client_type is set correctly.

### GLM (Zhipu) Specific Issues

GLM returns HTTP 401 for rate limiting (non-standard). The retry layer treats 401 from GLM as retryable. If persistent, the API key quota may be exhausted.

---

## Subagent Stuck / Watchdog

### Architecture
Each subagent has an independent watchdog timer (default 3 min). The timer resets ("feeds") when:
- LLM outputs tokens
- Bash command produces stdout/stderr output

If the timer expires, the subagent is terminated and retried.

### Diagnosis
```bash
# Look for subagent/watchdog entries
docker logs memoh-server --tail 200 | grep -i "spawn\|watchdog\|subagent"
```

### Common Issues

**Subagent result lost**: If idle timeout fires during spawn, the context gets cancelled but the spawn goroutine is still running. Fix: spawn goroutines use `context.WithoutCancel` so their results are preserved.

**Spawn goroutine leak**: Subagent retry creates `time.After` timers that are never cleaned up when the alternative path completes.

### Key Files
- `internal/agent/tools/subagent.go` — Spawn tool + watchdog
- `internal/agent/agent.go` — `runStream` goroutine lifecycle

---

## Goroutine Leak Detection

### Pattern
```go
// WRONG — blocks forever if consumer stops reading
ch <- StreamEvent{Type: "content", Content: "..."}

// CORRECT — protected by context cancellation
select {
case ch <- StreamEvent{Type: "content", Content: "..."}:
case <-ctx.Done():
    return
}
```

### Diagnosis
If the server's memory grows unbounded, check for goroutine leaks:
```bash
# If pprof is enabled
curl http://localhost:8080/debug/pprof/goroutine?debug=1

# Otherwise, check process goroutine count
docker compose exec server sh -c "kill -SIGUSR1 1"
docker logs memoh-server --tail 5
```

### Prevention
- All channel sends in streaming loops must use `select` + `ctx.Done()`
- Long-running child goroutines use `context.WithoutCancel` to preserve results
- Always clean up `time.After` / `time.NewTimer` when no longer needed

---

## Misskey Timeline Dedup Issues

### Architecture
When timeline subscriptions are active (homeTimeline and/or localTimeline), a dedup cache prevents the same note from being processed multiple times.

Sources of duplicate notes:
1. Same note appears in both homeTimeline and localTimeline
2. Same note arrives via main channel as mention/reply notification

### Dedup Mechanism
- `map[string]time.Time` — note ID → first-seen timestamp
- TTL: 2 minutes, cleanup interval: 30 seconds
- Covers all event types (timeline, mention, reply, notification)

### Diagnosis
```bash
# Check for duplicate processing
docker logs memoh-server --tail 100 | grep -i "timeline\|dedup\|duplicate"
```

### Key Files
- `internal/channel/adapters/misskey/misskey.go` — Dedup cache created in `runStreamLoop()`, passed to `runStream()`

---

## Frontend Changes Not Taking Effect

### Cause
The web frontend is a Docker image. Changes to Vue source code require rebuilding the image, not just restarting the container.

### Fix
```bash
# Rebuild web image
docker build -t memohai/web:latest -f docker/Dockerfile.web .

# Or use compose
docker compose build web

# Then recreate
docker compose up -d --force-recreate web
```

### Quick Check
```bash
# Is the web container using a rebuilt image?
docker inspect memoh-web --format '{{.Image}}'
```
