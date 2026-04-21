# Common Bug Patterns & Prevention

These patterns were discovered through production debugging sessions (Apr 7-21, 2026). Each pattern includes the symptom, root cause, fix, and a prevention rule.

---

## Pattern 1: Unbuffered Channel Goroutine Leak

### Symptom
Server memory grows unbounded over time. Bot eventually stops responding. No obvious error in logs.

### Root Cause
```go
// PROBLEM: Unbuffered blocking send
func runStream(ctx context.Context, ch chan StreamEvent) {
    for {
        event := getNextEvent()
        ch <- event  // Blocks until consumer reads
        // If consumer stopped reading (due to timeout), this goroutine blocks FOREVER
    }
}
```

When the consumer (e.g., resolver) stops reading due to idle timeout or context cancellation, the producer goroutine permanently blocks on the unbuffered send. The goroutine is leaked permanently.

### Fix
```go
func runStream(ctx context.Context, ch chan StreamEvent) {
    for {
        event := getNextEvent()
        select {
        case ch <- event:
            // Sent successfully
        case <-ctx.Done():
            return // Clean exit
        }
    }
}
```

### Prevention Rule
**Every channel send in a goroutine must be protected by `select` + `ctx.Done()`.** No exceptions for "short-lived" goroutines — they can always outlive their consumer.

**Key files**: `internal/agent/agent.go` (runStream method)

---

## Pattern 2: Context Cancel Propagation to Child Goroutines

### Symptom
Subagent or spawned task completes successfully, but its result is silently discarded. No error logged.

### Root Cause
```go
// PROBLEM: Parent context cancelled → child goroutine result lost
func (a *Adapter) execSpawn(ctx context.Context) {
    // ctx gets cancelled when idle timeout fires
    go func() {
        result := doWork()  // Completes successfully
        a.saveResult(ctx, result)  // But ctx is cancelled, save fails silently
    }()
}
```

When idle timeout fires, the parent context is cancelled. Child goroutines that are still running (spawned subagents, file operations) have their context cancelled too, causing their results to be discarded.

### Fix
```go
func (a *Adapter) execSpawn(ctx context.Context) {
    // Use context.WithoutCancel for long-running child work
    go func() {
        childCtx := context.WithoutCancel(ctx)  // Survives parent cancellation
        result := doWork()
        a.saveResult(childCtx, result)  // Result is preserved
    }()
}
```

### Prevention Rule
**Long-running child goroutines that need to preserve their result must use `context.WithoutCancel(ctx)`.** The parent's cancellation should signal "stop waiting" but not "abort work in progress."

**Key files**: `internal/agent/tools/subagent.go` (execSpawn)

---

## Pattern 3: Wall-Clock Timeout in Streaming Scenarios

### Symptom
Bot's stream is killed mid-response, especially during multi-tool calls or long responses. The bot was actively sending data but was timed out anyway.

### Root Cause
```go
// PROBLEM: Fixed wall-clock timeout
ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
// 120 seconds after creation, context is cancelled
// Even if the stream was actively sending events the whole time
```

A wall-clock timeout starts counting from creation, regardless of activity. Multi-tool calls (read file → edit → run command → read output) can legitimately take >120s while actively exchanging events.

### Fix
```go
// SOLUTION: Adaptive idle timeout that resets on each event
type idleCancel struct {
    cancel      context.CancelFunc
    timer       *time.Timer
    mu          sync.Mutex
    fired       bool
    baseTimeout time.Duration
    toolCalls   int
}

// Timeout adapts: base (90s) + 60s per tool call, capped at 600s
func (ic *idleCancel) currentTimeout() time.Duration {
    extra := time.Duration(ic.toolCalls) * 60 * time.Second
    timeout := ic.baseTimeout + extra
    if timeout > 600*time.Second {
        timeout = 600 * time.Second
    }
    return timeout
}

func (ic *idleCancel) Reset() {
    ic.mu.Lock()
    defer ic.mu.Unlock()
    if !ic.fired {
        ic.timer.Stop()
        ic.timer.Reset(ic.currentTimeout())
    }
}
```

### Prevention Rule
**Use idle timeout (per-event reset) for streaming, not wall-clock timeout.** Wall-clock timeout is only appropriate for non-streaming operations.

**Key files**: `internal/conversation/flow/idle_timeout.go`, `resolver_stream.go`

---

## Pattern 4: Cross-Source Event Deduplication

### Symptom
Same message triggers multiple bot responses. Users see 2-3 identical or slightly different replies to the same message.

### Root Cause
When multiple event sources can deliver the same event:
- Misskey: homeTimeline + localTimeline overlap (same note in both)
- Misskey: timeline + main channel (same note as mention/reply)
- Any platform: webhook + polling delivering same event

Each source processes independently, causing duplicate agent invocations.

### Fix
TTL-based dedup cache covering all sources:

```go
type dedupCache struct {
    mu   sync.Mutex
    seen map[string]time.Time
    ttl  time.Duration
}

func (d *dedupCache) checkAndMark(id string) bool {
    d.mu.Lock()
    defer d.mu.Unlock()
    if t, ok := d.seen[id]; ok && time.Since(t) < d.ttl {
        return true // duplicate, skip
    }
    d.seen[id] = time.Now()
    return false // first time, process
}
```

### Prevention Rule
**Whenever a system subscribes to multiple event sources that can overlap, add a unified dedup layer.** TTL cache with periodic cleanup is the simplest effective approach. The dedup must cover ALL sources, not just a subset.

**Key files**: `internal/channel/adapters/misskey/misskey.go` (runStream, handleChannelEvent)

---

## Pattern 5: Post-Success Trigger Deadlock

### Symptom
System enters an infinite retry loop. The operation keeps failing, and the mechanism that would fix it only triggers after success. Classic chicken-and-egg.

### Root Cause
```go
// PROBLEM: Compaction only triggers after successful LLM response
func (r *Resolver) resolve(ctx context.Context) {
    // Context too large for LLM to respond...
    result, err := r.agent.Stream(ctx)  // Always fails (context too large)
    if err == nil {
        r.maybeCompact()  // Never reached!
    }
}
```

Context compaction was designed to run after a successful response. But when context is too large for the LLM to ever respond, compaction never triggers → infinite failure loop.

### Fix
```go
func (r *Resolver) resolve(ctx context.Context) {
    // Proactive compaction BEFORE calling LLM
    if r.estimatedTokens > r.contextWindow*70/100 {
        r.compact(ctx)
    }

    result, err := r.agent.Stream(ctx)  // Now has a chance to succeed
    // ...
}
```

### Prevention Rule
**If an operation can fail in a way that makes it harder to succeed next time, the fix mechanism must be proactively triggered before the operation, not reactively after it.**

**Key files**: `internal/conversation/flow/resolver.go`, `resolver_compaction.go`

---

## Pattern 6: gci Import Ordering

### Symptom
Commit rejected by pre-commit hook with gci import ordering errors. The code compiles and runs fine locally.

### Root Cause
The `gci` linter enforces a specific import grouping order:
1. Standard library
2. Third-party
3. Local project

If imports are added/removed manually, the order may not match.

### Fix
```bash
# Auto-fix gci ordering for specific package
golangci-lint run --fix ./internal/channel/adapters/misskey/...

# Or for all packages
golangci-lint run --fix ./...
```

### Prevention Rule
**Run `golangci-lint run --fix` before committing Go code changes.** Always run lint checks locally before pushing.

---

## Pattern 7: Web Frontend Cache (Stale Docker Image)

### Symptom
Frontend UI changes (new features, settings) don't appear after deploying. The web interface looks the same as before.

### Root Cause
The web container serves a pre-built static bundle baked into the Docker image. Restarting the container (`docker compose restart web`) reuses the old image with the old bundle.

### Fix
```bash
# Must REBUILD the image, not just restart
docker compose build web
docker compose up -d --force-recreate web
```

### Prevention Rule
**Any frontend change requires `docker compose build web` before deployment.** Server-only changes only need server rebuild. When in doubt, rebuild both.

---

## Go Concurrency Safety Checklist

Before committing code that uses goroutines, channels, or context:

- [ ] **Channel sends**: All `ch <- value` protected by `select { case ch <- value: case <-ctx.Done(): return }`
- [ ] **Child goroutines**: Long-running work uses `context.WithoutCancel(ctx)` if result must be preserved
- [ ] **Timer cleanup**: All `time.After` / `time.NewTimer` cleaned up when no longer needed
- [ ] **Mutex scope**: Mutex locks released in `defer` statements, not at end of function
- [ ] **Map concurrent access**: Shared maps protected by mutex or use `sync.Map`
- [ ] **Goroutine lifecycle**: Every `go func()` has a clear exit condition via `ctx.Done()` or done channel
- [ ] **Error propagation**: Goroutine errors are reported back to caller, not silently swallowed
