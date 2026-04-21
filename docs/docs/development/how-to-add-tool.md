# How to Add a New Agent Tool

Agent tools are functions the AI can invoke during conversation (e.g., web search, file operations, send email). This guide shows how to create a new tool provider.

## Overview

Tools implement the `ToolProvider` interface and return LLM tool definitions with execute closures. The agent assembles available tools into the prompt, and the LLM decides when to call them.

## Step 1: Create the Tool Provider

Create a new file in `internal/agent/tools/<tool_name>.go`:

```go
package tools

import (
    "context"
    // ...
)

type MyToolProvider struct {
    log *slog.Logger
    // Dependencies needed by the tool
    someService *some.Service
}

func NewMyToolProvider(log *slog.Logger, svc *some.Service) *MyToolProvider {
    return &MyToolProvider{
        log:         log,
        someService: svc,
    }
}
```

## Step 2: Implement ToolProvider Interface

```go
// Interface definition (in types.go):
// type ToolProvider interface {
//     Tools(ctx context.Context, session SessionContext) ([]sdk.Tool, error)
// }

func (p *MyToolProvider) Tools(ctx context.Context, session SessionContext) ([]sdk.Tool, error) {
    // Return nil if tool shouldn't be available (e.g., missing dependency)
    if p.someService == nil {
        return nil, nil
    }

    return []sdk.Tool{
        {
            Name:        "my_tool",
            Description: "Description of what this tool does. Be specific — the LLM uses this to decide when to call it.",
            Parameters: map[string]any{
                "type": "object",
                "properties": map[string]any{
                    "query": map[string]any{
                        "type":        "string",
                        "description": "What to search for",
                    },
                    "limit": map[string]any{
                        "type":        "integer",
                        "description": "Maximum results to return",
                    },
                },
                "required": []string{"query"},
            },
            Execute: func(ctx context.Context, args map[string]any) (string, error) {
                return p.executeMyTool(ctx, session, args)
            },
        },
    }, nil
}
```

## Step 3: Implement Execute Logic

```go
func (p *MyToolProvider) executeMyTool(ctx context.Context, session SessionContext, args map[string]any) (string, error) {
    // Parse arguments using helpers
    query := StringArg(args, "query")
    if query == "" {
        query = FirstStringArg(args, "query", "q", "search") // fallback: try multiple keys
    }
    limit, hasLimit, err := IntArg(args, "limit")
    if err != nil || !hasLimit {
        limit = 10 // default
    }

    // Do the actual work
    results, err := p.someService.Search(ctx, query, limit)
    if err != nil {
        return "", fmt.Errorf("my_tool failed: %w", err)
    }

    // Return result as string (the LLM reads this)
    return formatResults(results), nil
}
```

## Argument Parsing Helpers

Available in `internal/agent/tools/types.go`:

| Helper | Signature | Description |
|--------|-----------|-------------|
| `StringArg(args, key)` | `string` | Get string argument by name |
| `FirstStringArg(args)` | `string` | Get first string value from any key |
| `IntArg(args, key, default)` | `int` | Get int argument with default |
| `BoolArg(args, key)` | `bool` | Get boolean argument |
| `inputAsMap(args)` | `map[string]any` | Normalize args to map |

## Step 4: Emit Side Effects (Optional)

Tools can emit attachments, reactions, or speech via `SessionContext.Emitter` (type `StreamEmitter`):

```go
// Attach a file to the response
session.Emitter(ToolStreamEvent{
    Type: StreamEventAttachment,
    Attachments: []Attachment{{
        Name: "result.png",
        Mime: "image/png",
        Path: "/tmp/result.png",
    }},
})

// Add a reaction
session.Emitter(ToolStreamEvent{
    Type: StreamEventReaction,
    Reactions: []Reaction{{Emoji: "👍"}},
})

// Trigger text-to-speech
session.Emitter(ToolStreamEvent{
    Type: StreamEventSpeech,
    Speeches: []Speech{{Text: "Here is what I found"}},
})
```

## Step 5: Register the Tool

Tools use **setter injection** to avoid FX dependency cycles.

In `cmd/agent/app.go` (FX wiring):

```go
// Create all tool providers
allProviders := []tools.ToolProvider{
    tools.NewMyToolProvider(log, someService),
    tools.NewContactsProvider(log, routeService),
    // ... etc
}

// Inject all providers at once via SetToolProviders
agentService.SetToolProviders(allProviders)
```

The agent has a single `SetToolProviders(providers []ToolProvider)` method that accepts a slice of all providers.

## Step 6: Tool Availability Control

### Per-Channel Tool Restrictions

Channels can restrict which tools are available via the `allowed_tools` field in `bot_channel_configs`:

```sql
-- In channel config
UPDATE bot_channel_configs
SET allowed_tools = '["send", "get_contacts", "web_search"]'
WHERE id = $1;
```

When implementing tools, check if your tool should respect this restriction. The tool assembly in the agent already filters by allowed_tools.

### Conditional Availability

Return `nil, nil` from `Tools()` to make a tool unavailable in certain conditions:

```go
func (p *MyToolProvider) Tools(ctx context.Context, session SessionContext) ([]sdk.Tool, error) {
    // Only available if feature is enabled
    if !someCondition {
        return nil, nil
    }

    return []sdk.Tool{...}, nil
}
```

## Step 7: Add Frontend Support (If Needed)

If the tool has user-configurable settings:
1. Add settings fields to `internal/settings/types.go`
2. Add UI in `apps/web/src/pages/bots/components/bot-settings.vue`
3. Persist via the settings API

## Best Practices

1. **Description matters**: The LLM uses tool descriptions to decide when to call them. Be specific about what the tool does and when to use it.

2. **Return format**: Return structured text (JSON, markdown tables, numbered lists) so the LLM can parse it easily.

3. **Error messages**: Return clear error messages — the LLM will relay them to the user or try alternative approaches.

4. **Context awareness**: Use `ctx` for cancellation. Long-running tools should check `ctx.Done()`.

5. **Idempotency**: Tools may be called multiple times with the same arguments. Design for idempotency where possible.

6. **No side effects in description/schema**: The `Execute` closure is where side effects happen, not during `Tools()` call.

## Reference Implementations

| Tool | File | Complexity | What it demonstrates |
|------|------|-----------|---------------------|
| `get_contacts` | `contacts.go` | Simple | Basic tool with optional filter parameter |
| `send_message` | `message.go` | Medium | Multi-parameter, channel routing |
| `web_search` | `web.go` | Medium | External API call, result formatting |
| `container_exec` | `container.go` | Complex | Workspace interaction, streaming output |
| `spawn` | `subagent.go` | Complex | Watchdog, retry, parallel subagents |
| `browser_action` | `browser.go` | Complex | Browser automation via gateway service |
