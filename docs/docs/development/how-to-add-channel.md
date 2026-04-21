# How to Add a New Channel Adapter

This guide walks through implementing a new platform adapter (e.g., Slack, IRC, Matrix) for Memoh.

## Overview

Channel adapters translate between platform-specific APIs and Memoh's internal message format. The system uses **optional interface composition** — implement only the capabilities your platform supports.

## File Structure

Create a new package under `internal/channel/adapters/<platform>/`:

```
internal/channel/adapters/<platform>/
├── descriptor.go    # ChannelType constant + basic metadata
├── config.go        # Config struct + parsing from ChannelConfig
├── <platform>.go    # Main adapter struct, implements core interfaces
├── stream.go        # (optional) WebSocket/streaming support
├── directory.go     # (optional) User/directory lookup
└── markdown.go      # (optional) Markdown conversion helpers
```

## Step 1: Define the Channel Type

`descriptor.go`:

```go
package slack

import "github.com/memohai/memoh/internal/channel"

const ChannelTypeSlack channel.ChannelType = "slack"
```

## Step 2: Define Config Struct

`config.go`:

```go
type slackConfig struct {
    BotToken      string
    AppToken      string // for socket mode
    SigningSecret string
}

func parseConfig(cfg channel.ChannelConfig) (slackConfig, error) {
    c := slackConfig{}
    // Parse from cfg fields (e.g., cfg.Token, cfg.AppID, cfg.Extra)
    // or from cfg.Routing JSONB for extended config
    return c, nil
}
```

## Step 3: Implement the Adapter

`<platform>.go`:

```go
package slack

type SlackAdapter struct {
    log       *slog.Logger
    config    slackConfig
    session   *channel.Session
}

// Required: Adapter interface
func (a *SlackAdapter) Type() channel.ChannelType {
    return ChannelTypeSlack
}

func (a *SlackAdapter) Descriptor() channel.Descriptor {
    return channel.Descriptor{
        Type:        ChannelTypeSlack,
        DisplayName: "Slack",
        // Capabilities, OutboundPolicy, ConfigSchema, etc.
    }
}
```

## Step 4: Implement Capability Interfaces

Pick the interfaces your platform supports:

| Interface | Methods | What it does |
|-----------|---------|-------------|
| `Sender` | `Send(ctx, cfg, msg PreparedOutboundMessage) error` | Send text/media messages |
| `Receiver` | `Connect(ctx, cfg, handler InboundHandler) (Connection, error)` | Long-lived connection for inbound messages |
| `StreamSender` | `OpenStream(ctx, cfg, target, opts) (PreparedOutboundStream, error)` | SSE streaming replies |
| `MessageEditor` | `Update(ctx, cfg, target, msgID, msg) error` + `Unsend(...)` | Edit/delete sent messages |
| `Reactor` | `React(ctx, cfg, target, msgID, emoji) error` + `Unreact(...)` | Add/remove emoji reactions |
| `ConfigNormalizer` | `NormalizeConfig(raw) (map, error)` + `NormalizeUserConfig(...)` | Validate/normalize config |
| `TargetResolver` | `NormalizeTarget(raw) string` + `ResolveTarget(userConfig) (string, error)` | Resolve delivery targets |
| `BindingMatcher` | `MatchBinding(config, criteria) bool` + `BuildUserConfig(Identity)` | Match channel identity to user |
| `WebhookReceiver` | `HandleWebhook(ctx, cfg, handler, r, w) error` | Handle webhook callbacks |
| `SelfDiscoverer` | `DiscoverSelf(ctx, credentials) (identity, externalID, error)` | Get bot's own identity |
| `AttachmentResolver` | `ResolveAttachment(ctx, cfg, attachment) (AttachmentPayload, error)` | Download attachments |
| `ProcessingStatusNotifier` | `ProcessingStarted` + `ProcessingCompleted` + `ProcessingFailed` | Show processing status |

Example — implementing `Sender` and `Receiver`:

```go
// Sender: send outbound messages
func (a *SlackAdapter) Send(ctx context.Context, cfg channel.ChannelConfig, msg channel.PreparedOutboundMessage) error {
    // Convert msg to platform format and send
    _, _, err := a.client.PostMessage(target, slack.MsgOptionText(msg.Text, false))
    return err
}

// Receiver: establish long-lived connection
func (a *SlackAdapter) Connect(ctx context.Context, cfg channel.ChannelConfig, handler channel.InboundHandler) (channel.Connection, error) {
    ctx, cancel := context.WithCancel(ctx)
    go func() {
        defer cancel()
        // Listen for platform events
        // Convert to InboundMessage and call handler(ctx, cfg, msg)
        // Use ctx.Done() for clean shutdown
    }()
    return channel.NewConnection(cfg, func(ctx context.Context) error {
        cancel()
        return nil
    }), nil
}
```

## Step 5: Register the Adapter

In the FX module setup (e.g., `internal/channel/module.go` or the adapter's own init):

```go
fx.Provide(
    func(log *slog.Logger) *SlackAdapter {
        return &SlackAdapter{log: log}
    },
),
```

And register with the channel registry:

```go
registry.MustRegister(adapter)
```

## Step 6: Build InboundMessage

When converting platform events to `InboundMessage`:

```go
msg := channel.InboundMessage{
    Channel:     ChannelTypeSlack,
    BotID:       botID,
    ReplyTarget: messageID,     // For threading/reply
    RouteKey:    routeKey,      // Routing key: platform:bot_id:conversation_id[:sender_id]
    Sender: channel.Identity{
        SubjectID:   userID,
        DisplayName: username,
    },
    Conversation: channel.Conversation{
        ID:   conversationID,
        Type: channel.ConversationTypePrivate,
    },
    Message: channel.Message{
        Text:        text,
        Attachments: []channel.Attachment{...},
    },
    Metadata: map[string]any{
        "is_timeline": false,   // Set true for timeline/social feed messages
        // Any extra platform-specific data
    },
}
```

## Timeline / Social Feed Pattern

If the platform has a timeline/feed feature (like Misskey), implement:

### Subscription
Subscribe to timeline channels in the WebSocket/streaming loop.

### Filtering
Filter out unwanted events:
- Own messages
- Pure reposts without commentary
- Very short messages (< 5 chars)
- Direct messages if only handling public timeline

### Deduplication
Use a TTL-based dedup cache when the same event can arrive from multiple sources:

```go
type dedupCache struct {
    mu    sync.Mutex
    seen  map[string]time.Time
    ttl   time.Duration
}

func (d *dedupCache) isDuplicate(id string) bool {
    d.mu.Lock()
    defer d.mu.Unlock()
    if t, ok := d.seen[id]; ok && time.Since(t) < d.ttl {
        return true // duplicate
    }
    d.seen[id] = time.Now()
    return false
}
```

### Metadata Propagation
Set `is_timeline` metadata so the pipeline correctly handles timeline messages (bypasses talk_value threshold and timing gate):

```go
msg.Metadata["is_timeline"] = true
msg.Metadata["timeline_source"] = "home" // or "local"
```

### Unified Conversation
Use a fixed `RouteKey` like `"timeline"` to aggregate all timeline messages into one conversation.

### Routing Config (No DB Migration)
Store timeline configuration in the existing `routing` JSONB field:

```go
// Read from channel config
routing := cfg.Routing // map[string]any
timelineCfg := routing["timeline"]  // map[string]any{"home": true, "local": false}
```

## Frontend: Channel Settings UI

Add platform-specific settings in `apps/web/src/pages/bots/components/channel-settings-panel.vue`:

1. Add reactive form fields for your platform's config
2. Load from `props.channelItem.config?.routing?.<yourplatform>`
3. Save back to the same path
4. Use `v-if="isPlatform('yourplatform')"` to show only for your platform

## Testing

1. Unit test config parsing
2. Test message normalization (platform event → InboundMessage)
3. Test dedup logic if implementing timeline
4. Manual test: create bot with your channel, send messages

## Reference Implementations

| Adapter | Complexity | Good for learning |
|---------|-----------|------------------|
| `local/` | Minimal | Basic sender/receiver only |
| `misskey/` | Full | WebSocket, timeline, dedup, reactions, streaming |
| `telegram/` | Full | All 12 capability interfaces |
| `discord/` | Full | Gateway WebSocket, slash commands |
