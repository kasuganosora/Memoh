# Memoh 融入 MaiSaka 交互特征计划 (修正版 v3)

> 基于 `MaiBot/docs/design_analysis_zh.md` 的分析 + Memoh 现有代码的深度复核，将 MaiSaka 四大核心特征融入 Memoh 的具体技术方案。
>
> **v2→v3 修正**：Replyer 从独立 Provider 合并入 `MessageProvider`（消除 send/reply 语义冲突）、LLM 注入路径显式参考 `TimingGate` 模式、补全 `UpsertRequest`/`DiscussTriggerDeps` 的 DI 变更、Expression Selector embedding 路径明确化。

---

## 一、Memoh 现状与差距（复核后）

| 特征 | MaiSaka | Memoh 现状 | 差距 |
|------|---------|-----------|------|
| 自然对话风格 | Planner(reason)+Replyer(re-gen) | discuss 模式已有 monologue/send 分离，`send` 直接输出 Planner 原文 | **小**—仅需在 send 工具中加入 replyer 重生成逻辑 |
| 时机控制 | Timing Gate 子代理 (continue/wait/no_reply) | **`internal/chattiming/timing_gate.go` 已完整实现**，已集成到 `pipeline/discuss_trigger.go`，有 enable/disable 配置 | **已完成**—只需确认 chat 模式下的扩展路径 |
| 风格/黑话学习 | Expression Learner + Jargon Miner (离线) | `discuss_trigger.go` 已有 `extractPassiveMemory` 模式（不说话时提取记忆），可作为学习触发点 | **大**—需新建学习系统 |
| 人格记忆 | A_Memorix (向量+图+画像) | Provider 接口 (OnBeforeChat/OnAfterChat/Search)，有被动记忆提取 | **中等**—需增强为用户画像 + 多模式检索 |

### 1.1 现有基础设施速览

```
内部已有：
  internal/chattiming/           ← Timing Gate + 智能时机（完整）
  ├── timing_gate.go             # TimingGate.Evaluate(continue/wait/no_reply)
  ├── config.go                  # Config{TimingGate,Debounce,TalkValue,Interrupt,...}
  ├── service.go                 # NewTimingGate(), NewDebouncer() 等
  ├── debounce.go, interrupt.go, talk_value.go, idle_compensation.go
  internal/pipeline/discuss_trigger.go  ← Timing Gate 的消费端
  │   evaluateTimingGate()       # 已有 complete LLM-gate 调用
  │   extractPassiveMemory()     # 不说话时提取记忆的模式
  │   wireSmartTiming()          # 从 settings 读取 chat_timing 配置
  internal/settings/             ← 特性开关基础设施
  │   types.go: Settings{ChatTiming ChatTimingConfig}
  │   service.go: GetBot(), UpsertBot()
  internal/agent/tools/types.go  ← ToolProvider 接口（扩展点）
  internal/memory/adapters/      ← Provider 接口（OnBeforeChat/OnAfterChat/Search）
  internal/agent/prompts/        ← 嵌入式 prompt 模板（{{key}} 语法）
  internal/agent/agent.go        ← Agent 核心循环（**不应修改**）
```

---

## 二、Phase 1：Replyer 重生成（自然对话风格）

### 2.1 设计原则

**核心理念**：Replyer 作为 ToolProvider 内部的 LLM 调用完成重生成，**零侵入 Agent 核心循环**。这与现有的 `spawn_adapter.go`（子代理工具内部调用 LLM）模式一致。

```
改造前：LLM → [推理] → send(text="原始推理结果") → 直接发送
改造后：LLM → [推理] → reply(reasoning="推理摘要") → 工具内部调小LLM → 自然语言 → send
```

### 2.2 修改方案

#### 2.2.1 修改 `system_discuss.md` — 替换 send 为 reply（当 replyer 启用时）

当前 `system_discuss.md:26-27` 说 `send` 是唯一输出方式。修改为条件性提示：

```markdown
## How to Reply

When replyer is active, the `reply` tool replaces `send`:
- `reply(reasoning="<your reasoning summary>")` — your reasoning will be polished into
  natural language by a replyer. Do NOT call `send` directly.
- The replyer will re-generate a casual, human-like reply from your reasoning.

When replyer is not active, use `send` directly as before.
```

> **注意**：此项修改通过 prompt 变量注入，不直接修改 `.md` 文件。由 `prepareRunConfig` 动态决定注入哪一段。

#### 2.2.2 扩展 MessageProvider（合并 Replyer 逻辑）

**不新建独立文件**。Replyer 逻辑直接合并进现有的 `internal/agent/tools/message.go`。

> **架构决策**：如新建独立的 `ReplyerProvider`，它会与 `MessageProvider` 并列返回工具，造成 LLM 同时看到 `send` 和 `reply` 两个语义冲突的工具。`MessageProvider` 已持有 `messaging.Executor`（消息发送的全部逻辑），把 `reply` 加入它是语义内聚的。

**LLM 调用注入模式**：参考 `chattiming/timing_gate.go:62` —— `TimingGate` 直接持有 `*agentpkg.Agent`，通过 `agent.Generate()` 发起 LLM 调用。`MessageProvider` 同样注入 `*agentpkg.Agent` 来完成 replyer 重生成：

```go
// internal/agent/tools/message.go

import agentpkg "github.com/memohai/memoh/internal/agent"

type MessageProvider struct {
    exec          *messaging.Executor
    agent         *agentpkg.Agent          // ← 新增：LLM 调用（与 TimingGate 模式一致）
    settings      SettingsReader            // ← 新增：读取 EnableReplyer/ReplyerModelID
    exprSelector  *expression.ExpressionSelector // ← 新增：可选，Phase 3 完成后接入
    logger        *slog.Logger
}

func NewMessageProvider(log *slog.Logger, sender messaging.Sender, /* ...existing args... */,
    agent *agentpkg.Agent,           // ← 新增参数
    settings SettingsReader,          // ← 新增参数
    exprSelector *expression.ExpressionSelector, // ← 可选，nil 表示未启用
) *MessageProvider {
    return &MessageProvider{
        exec:     &messaging.Executor{ /* ...existing... */ },
        agent:    agent,
        settings: settings,
        exprSelector: exprSelector,
        logger:   log.With(slog.String("tool", "message")),
    }
}
```

**Tools() 方法变更**：当 replyer 启用时返回 `reply` 而不是 `send`：

```go
func (p *MessageProvider) Tools(ctx context.Context, session SessionContext) ([]sdk.Tool, error) {
    if session.IsSubagent {
        return nil, nil
    }
    var tools []sdk.Tool

    // 读取 bot 设置决定返回 send 还是 reply
    useReplyer := p.settings != nil &&
        session.SessionType == "discuss" &&
        p.settings.GetBool(ctx, session.BotID, "enable_replyer")

    if p.exec.CanSend() {
        if useReplyer {
            tools = append(tools, p.replyTool(session))   // reply 替代 send
        } else {
            tools = append(tools, p.sendTool(session))     // 原有 send
        }
    }
    if p.exec.CanReact() {
        tools = append(tools, p.reactTool(session))
    }
    return tools, nil
}

func (p *MessageProvider) replyTool(session SessionContext) sdk.Tool {
    return sdk.Tool{
        Name:        "reply",
        Description: "Generate and send a visible reply. Provide your reasoning summary; it will be polished into natural conversational language by a replyer.",
        Parameters: map[string]any{
            "type": "object",
            "properties": map[string]any{
                "reasoning": map[string]any{"type": "string", "description": "Brief summary of your analysis and what you want to express"},
                "reply_to":  map[string]any{"type": "string", "description": "Message ID to reply to (optional)"},
            },
            "required": []string{"reasoning"},
        },
        Execute: func(ctx *sdk.ToolExecContext, input any) (any, error) {
            return p.execReply(ctx.Context, session, inputAsMap(input))
        },
    }
}

func (p *MessageProvider) execReply(ctx context.Context, session SessionContext, args map[string]any) (any, error) {
    reasoning := StringArg(args, "reasoning")
    replyTo := StringArg(args, "reply_to")

    // 1. 读取 replyer model（从 settings，为空则复用 chat_model）
    // 2. 构建 replyer system prompt + messages（只读最近历史 + reasoning）
    // 3. 调用小模型（*agentpkg.Agent.Generate）生成自然语言 ← 与 TimingGate 完全一致的模式
    // 4. 注入表达方式（如果 ExpressionSelector 有匹配结果）

    replyText, err := p.generateReply(ctx, session, reasoning)
    if err != nil {
        replyText = reasoning  // 回退：直接发送 reasoning 原文
    }
    return p.exec.Send(ctx, toMessagingSession(session), map[string]any{
        "text":     replyText,
        "reply_to": replyTo,
    })
}

// sendTool 是原 send 工具的提取（不修改逻辑，仅从 Tools() 内联重构为方法）
func (p *MessageProvider) sendTool(session SessionContext) sdk.Tool {
    return sdk.Tool{
        Name:        "send",
        // ... 原有参数定义不变 ...
        Execute: func(ctx *sdk.ToolExecContext, input any) (any, error) {
            return p.execSend(ctx.Context, session, inputAsMap(input))
        },
    }
}
```

**关键设计决策**：
- **不修改 `agent.go`**：replyer LLM 调用发生在 `execReply()` 内部，`agent.go` 零改动
- **回退机制**：replyer 失败时直接发送 reasoning 原文，保证可用性
- **单一工具返回**：`Tools()` 根据设置返回 `send` **或** `reply`，LLM 永远不会同时看到两者
- **LLM 注入路径**：`*agentpkg.Agent` 直接注入 `MessageProvider`，与 `TimingGate`（`chattiming/timing_gate.go:62`）的 `*agentpkg.Agent` 注入模式完全一致
- **Expression 注入可拔插**：`exprSelector` 为 nil 时跳过，Phase 3 完成后传入非 nil 即可自动启用

#### 2.2.3 新增 Replyer 系统提示

**新建**：`internal/agent/prompts/system_replyer.md`

```markdown
You are a **replyer** — your job is to turn the bot's internal reasoning into a
natural, conversational reply. Read the chat history and the reasoning below,
then produce ONLY the reply text.

Rules:
- Write as a casual human, not a bot
- NO markdown, NO bullet points, NO JSON, NO code blocks
- NO parentheses, colons, or @ mentions  
- Match the tone and energy of the conversation
- Keep it short unless context demands detail
- Output ONLY the message content — nothing else
```

#### 2.2.4 表达方式注入到 Replyer

当 Expression 学习系统有数据时（见 Phase 3），将匹配的表达式注入 replyer 的 system prompt 尾部：

```markdown
## Style Reference

The bot's past expressions in similar situations:
- Situation: "对某事表示惊叹" → Style: "我嘞个..."
- Situation: "表达赞同" → Style: "确实确实"

Match your reply to these styles naturally. Don't force them if they don't fit.
```

注入由 `ExpressionSelector` 完成（见 Phase 3），在 replyer 的 system prompt 构建时拼接。

### 2.3 关键修改文件清单

```
新增:
  internal/agent/prompts/system_replyer.md      — replyer 系统提示

修改:
  internal/agent/tools/message.go               — MessageProvider 扩展：注入 *Agent，Tools() 返回 reply/send 二选一
  internal/conversation/flow/resolver.go        — prepareRunConfig 中注入 replyer 配置
  internal/settings/types.go                    — 新增 EnableReplyer + ReplyerModelID 字段
```

**不修改的文件（与初版关键区别）**：
- `internal/agent/agent.go` — **零修改**
- `internal/agent/tools/types.go` — 不需要新增 SessionType（已存在）
- `internal/agent/tools/replyer.go` — **不存在**（逻辑合入 message.go）

### 2.4 Settings 扩展

**Settings 新增字段**：

```go
// internal/settings/types.go
type Settings struct {
    // ... existing fields ...
    EnableReplyer   bool   `json:"enable_replyer"`
    ReplyerModelID  string `json:"replyer_model_id"`  // 为空则使用 chat_model
}
```

**UpsertRequest 同步扩展**（保证 API 可写）：

```go
// internal/settings/types.go
type UpsertRequest struct {
    // ... existing fields ...
    EnableReplyer   *bool   `json:"enable_replyer,omitempty"`
    ReplyerModelID  string  `json:"replyer_model_id,omitempty"`
}
```

> **注意**：`UpsertRequest` 使用指针类型是可选的（`*bool`），这是现有 `ReasoningEnabled *bool` 的模式。如果决定使用零值语义（false=禁用），可以用非指针 `bool`。

---

## 三、Phase 2：Timing Gate（时机决策）→ 现状已实现，仅需扩展

### 3.1 已有实现概述

Memoh **已完整实现** Timing Gate 及其配套的智能时机系统：

| 组件 | 文件 | 状态 |
|------|------|------|
| `TimingGate.Evaluate()` | `internal/chattiming/timing_gate.go` | ✅ 完成 — continue/wait/no_reply 三决策 |
| `TimingGateParams` | `internal/chattiming/timing_gate.go:36` | ✅ 包含 IsMentioned, NewMsgCount, TalkValue, BotName 等 |
| `evaluateTimingGate()` | `internal/pipeline/discuss_trigger.go:275` | ✅ 已集成到 discuss 会话循环 |
| `wireSmartTiming()` | `internal/pipeline/discuss_trigger.go:656` | ✅ 从 settings 读取配置 |
| `chattiming.Config` | `internal/chattiming/config.go` | ✅ TimingGate, Debounce, TalkValue, Interrupt 等配置项 |
| `extractPassiveMemory()` | `internal/pipeline/discuss_trigger.go:612` | ✅ 不说话时的被动记忆提取 |

### 3.2 三条决策的处理流程（已实现）

```
新消息到达 DiscussTrigger.runSession()
  ├─ debounce: 等待安静期
  ├─ talk_value 阈值检查 (chattiming/talk_value.go)
  │   └─ 消息不够 → extractPassiveMemory() → 跳过
  ├─ timing gate (chattiming/timing_gate.go)
  │   ├─ isMentioned? → 直接 continue（无需 LLM 调用）
  │   ├─ 小模型调用 → {decision: "continue|wait|no_reply"}
  │   ├─ no_reply → extractPassiveMemory() → 返回
  │   └─ wait(n)  → time.After(n) → 重新检查
  └─ handleReplyWithAgent() → 正式 LLM 调用
```

### 3.3 待扩展：Chat 模式支持

当前 Timing Gate 仅在 **discuss 模式** 下运行。对于常规 chat 模式（一问一答），可作为可选项添加：

```
chat 模式 (resolver.StreamChat):
  resolve() → [可选: Timing Gate?] → agent.Stream() → storeRound()
```

**方案**：在 `resolve()` 之后、`agent.Stream()` 之前插入可选检查，如果 Timing Gate 决定 `no_reply`，直接返回空流。但 chat 模式通常是一对一会话，timing gate 的价值有限——discuss 模式才是它的主场。**建议 Phase 2 不做 code 修改，仅做文档化**。

### 3.4 关键文件

```
无需新增:
  internal/chattiming/timing_gate.go           — 已实现
  internal/chattiming/config.go                — 已实现
  internal/pipeline/discuss_trigger.go         — 已集成

可选扩展:
  internal/conversation/flow/resolver_stream.go — （可选）chat 模式的 gate 前置检查
```

---

## 四、Phase 3：表达方式与黑话学习系统

### 4.1 目标

建立离线学习系统，自动从聊天历史提取：
- **表达方式**（Expression）："情境 → 语言风格" 映射
- **黑话/俚语**（Jargon）：群聊特有的缩写、黑话

### 4.2 DDD 分层设计

```
internal/expression/               ← 领域层
├── entry.go                       # ExpressionEntry, JargonEntry 值对象
├── repository.go                  # ExpressionRepository, JargonRepository 接口
├── learner.go                     # 领域服务：学习逻辑（纯业务，不依赖 DB）
└── selector.go                    # 领域服务：向量匹配表达方式

internal/expression/postgres/      ← 基础设施层
├── expression_store.go            # PostgreSQL 实现 ExpressionRepository
└── jargon_store.go                # PostgreSQL 实现 JargonRepository

internal/agent/tools/
└── jargon.go                      # 接口层：query_jargon 工具 (ToolProvider)
```

### 4.3 领域实体

```go
// internal/expression/entry.go
package expression

type ExpressionEntry struct {
    ID         string
    BotID      string
    SessionID  string
    Situation  string  // "对某件事表示惊叹"
    Style      string  // "我嘞个xxxx"
    Count      int
    Checked    bool
    Rejected   bool
    CreatedAt  time.Time
    LastActive time.Time
}

type JargonEntry struct {
    ID        string
    BotID     string
    SessionID string
    Content   string  // "yyds"
    Meaning   string  // "永远的神"
    Count     int
    CreatedAt time.Time
}
```

### 4.4 仓储接口

```go
// internal/expression/repository.go
package expression

type ExpressionRepository interface {
    Upsert(ctx context.Context, expr ExpressionEntry) error
    SearchBySituation(ctx context.Context, botID string, situationEmbed []float64, topK int) ([]ExpressionEntry, error)
    ListUnchecked(ctx context.Context, botID string, limit int) ([]ExpressionEntry, error)
    MarkChecked(ctx context.Context, id string, rejected bool) error
}

type JargonRepository interface {
    Upsert(ctx context.Context, j JargonEntry) error
    Query(ctx context.Context, botID string, words []string) ([]JargonEntry, error)
    List(ctx context.Context, botID string, limit int) ([]JargonEntry, error)
}
```

### 4.5 存储层

```sql
-- db/migrations/00xx_add_expressions.sql

CREATE TABLE bot_expressions (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    bot_id      UUID NOT NULL REFERENCES bots(id) ON DELETE CASCADE,
    session_id  TEXT,
    situation   TEXT NOT NULL,
    style       TEXT NOT NULL,
    count       INT NOT NULL DEFAULT 1,
    checked     BOOL NOT NULL DEFAULT false,
    rejected    BOOL NOT NULL DEFAULT false,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_active TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- 核心查询索引
CREATE INDEX idx_bot_expressions_bot_situation
    ON bot_expressions(bot_id, checked, rejected, count DESC);
CREATE INDEX idx_bot_expressions_bot_lastactive
    ON bot_expressions(bot_id, last_active DESC);


-- db/migrations/00xx_add_jargons.sql

CREATE TABLE bot_jargons (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    bot_id      UUID NOT NULL REFERENCES bots(id) ON DELETE CASCADE,
    session_id  TEXT,
    content     TEXT NOT NULL,
    meaning     TEXT,
    count       INT NOT NULL DEFAULT 1,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX idx_bot_jargons_unique
    ON bot_jargons(bot_id, content);
CREATE INDEX idx_bot_jargons_bot
    ON bot_jargons(bot_id, count DESC);
```

### 4.6 学习服务

```go
// internal/expression/learner.go
package expression

type Learner struct {
    botID    string
    llm      LLMService     // 调用 LLM 提取
    exprRepo ExpressionRepository
    jargRepo JargonRepository
    pending  int32          // atomic — 累积消息数
    mu       sync.Mutex     // 防止并发学习
}

const minMessagesToLearn = 10 // 至少 10 条新消息才触发学习

func (l *Learner) Accumulate(ctx context.Context, messages []Message) {
    // 原子增加 pending 计数
    newPending := atomic.AddInt32(&l.pending, int32(len(messages)))

    if newPending >= minMessagesToLearn && l.mu.TryLock() {
        go func() {
            defer l.mu.Unlock()
            defer func() {
                if r := recover(); r != nil {
                    // log panic
                }
            }()
            atomic.StoreInt32(&l.pending, 0)
            l.LearnFromHistory(ctx)
        }()
    }
}

func (l *Learner) LearnFromHistory(ctx context.Context) error {
    // 1. 获取最近消息（只读用户消息，排除 bot 自己的）
    // 2. 调用 LLM with learn_style prompt → {expressions: [...], jargons: [...]}
    // 3. 去重、存储
    // 4. 错误时静默返回（不影响主流程）
}
```

**关键安全机制**：
- `sync.Mutex` 确保同一 bot 同一时间只有一次学习调用
- `atomic` 计数器无锁累加
- `TryLock` 非阻塞检查，已有学习进行中则跳过
- `recover()` 防止 goroutine panic 崩溃整个服务

### 4.7 表达方式选择器（向量搜索优先）

```go
// internal/expression/selector.go
package expression

type ExpressionSelector struct {
    repo   ExpressionRepository
    embedder Embedder  // 文本 → 向量（复用 Qdrant 或内置 embedding）
}

func (s *ExpressionSelector) Select(ctx context.Context, botID string, conversationCtx string, topK int) ([]ExpressionEntry, error) {
    // 1. 将当前对话上下文向量化
    ctxVec := s.embedder.Embed(ctx, conversationCtx)

    // 2. 向量相似度搜索（按 situation 语义匹配）
    entries, err := s.repo.SearchBySituation(ctx, botID, ctxVec, topK)
    if err != nil || len(entries) == 0 {
        // 3. 回退：关键词匹配
        entries, err = s.repo.SearchByKeywords(ctx, botID, extractKeywords(conversationCtx), topK)
    }
    return entries, err
}
```

**设计决策**：优先使用 embedding 向量搜索（无额外 LLM 调用，复用 Qdrant），仅当向量搜索无结果时才回退到关键词。不需要 MaiSaka 式子的子代理——节省一次 LLM 调用。

### 4.8 黑话查询工具

```go
// internal/agent/tools/jargon.go
package tools

type JargonProvider struct {
    jargonRepo expression.JargonRepository
    logger     *slog.Logger
}

func (p *JargonProvider) Tools(_ context.Context, session SessionContext) ([]sdk.Tool, error) {
    return []sdk.Tool{{
        Name:        "query_jargon",
        Description: "Look up the meaning of slang, abbreviations, or inside jokes used in the chat.",
        Parameters: map[string]any{
            "type": "object",
            "properties": map[string]any{
                "words": map[string]any{
                    "type": "array",
                    "items": map[string]any{"type": "string"},
                    "description": "Words or phrases to look up",
                },
            },
            "required": []string{"words"},
        },
        Execute: func(ctx *sdk.ToolExecContext, input any) (any, error) {
            return p.execQuery(ctx.Context, session, inputAsMap(input))
        },
    }}, nil
}
```

### 4.9 运行时机

重用现有的 `extractPassiveMemory` 模式（`discuss_trigger.go:612`）：

```go
// 在 storeRound 或 extractPassiveMemory 中：
// 当 bot 不说话（no_reply）时，积累用户消息，
// 达到阈值后触发 Expression Learner
go func() {
    defer func() { recover() }()
    learner.Accumulate(ctx, userMessages)
}()
```

**对于 chat 模式**：在 `resolver_store.go` 的 `storeRound` 中，`storeMemory` 旁边增加 `expressionLearner.Accumulate` 调用，使用 `context.WithoutCancel(ctx)` 确保学习不被请求取消打断（与 `storeMemory` 的行为一致）：

```go
// resolver_store.go:50-52（修改后）
r.storeMessages(ctx, req, filtered, modelID)
go r.storeMemory(context.WithoutCancel(ctx), req, filtered)          // 现有
go r.expressionLearner.Accumulate(context.WithoutCancel(ctx), filtered) // 新增
```

### 4.10 依赖注入变更：DiscussTriggerDeps

在 `discuss_trigger.go` 中增加表达学习支持，需要扩展 `DiscussTriggerDeps`：

```go
// pipeline/discuss_trigger.go（修改后）
type DiscussTriggerDeps struct {
    // ... existing fields ...
    MemoryFormation adapters.Provider     // 现有：被动记忆提取
    ChatTimingService  *chattiming.Service  // 现有：智能时机

    // --- Phase 3 新增 ---
    ExpressionLearner  *expression.Learner  // 新增：表达方式学习
    ExpressionSelector *expression.ExpressionSelector // 新增：可选，供 Replyer 使用
}
```

在 `extractPassiveMemory()` 中增加学习调用（不改动现有逻辑）：

```go
func (d *DiscussTrigger) extractPassiveMemory(ctx context.Context, sess *discussSession, rc RenderedContext, log *slog.Logger) {
    // ... 现有的被动记忆提取逻辑不变 ...

    // 新增：表达学习（在同一个 goroutine 中追加，避免增设 goroutine）
    if d.deps.ExpressionLearner != nil && len(messages) > 0 {
        d.deps.ExpressionLearner.Accumulate(d.parentCtx, messages)
    }
}
```

### 4.11 表达选择器的 Embedding 路径

`ExpressionSelector` 需要 embedding 能力以做向量相似度匹配。两种获取路径：

| 路径 | 方式 | 适用场景 |
|------|------|---------|
| **方案 A**：注入独立 `Embedder` | 新建轻量 embedding 客户端（调用同一模型 API） | 通用，需新增配置 |
| **方案 B**：复用 Memory Provider embedding | 扩展 `Provider` 接口（或内置 adapter 暴露 embed 方法） | 如果 Qdrant adapter 已暴露 embed |

**推荐方案 A**：`ExpressionSelector` 注入独立 `Embedder`（调用 OpenAI/Anthropic 同一模型），与 Memory Provider 解耦。embedding 调用频率低（仅在 replyer 阶段触发，每轮至多一次），性能开销可控。

```go
// internal/expression/selector.go
type ExpressionSelector struct {
    repo     ExpressionRepository
    embed    Embedder  // 独立注入，不依赖 Memory Provider
    // Embedder 接口：Embed(ctx, text string) ([]float64, error)
}
```

### 4.12 Settings 扩展

**Settings 新增字段**：

```go
// internal/settings/types.go
type Settings struct {
    // ... existing fields ...
    EnableExpressionLearn bool `json:"enable_expression_learning"`
}
```

**UpsertRequest 同步扩展**：

```go
type UpsertRequest struct {
    // ... existing fields ...
    EnableExpressionLearn *bool `json:"enable_expression_learning,omitempty"`
}
```

### 4.13 关键修改文件清单

```
新增:
  internal/expression/entry.go              — 领域实体
  internal/expression/repository.go          — 仓储接口
  internal/expression/learner.go            — 学习领域服务
  internal/expression/selector.go           — 表达选择器（向量搜索）
  internal/expression/embedder.go           — Embedder 接口 + 默认实现
  internal/expression/postgres/expression_store.go  — PG 仓储实现
  internal/expression/postgres/jargon_store.go      — PG 仓储实现
  internal/agent/tools/jargon.go            — query_jargon 工具

  db/migrations/00xx_add_expressions.sql
  db/migrations/00xx_add_jargons.sql
  db/queries/expressions.sql
  db/queries/jargons.sql

修改:
  internal/conversation/flow/resolver_store.go        — 增加表达学习钩子
  internal/pipeline/discuss_trigger.go                — DiscussTriggerDeps + extractPassiveMemory 增加学习触发
  internal/settings/types.go                          — 新增 EnableExpressionLearn 字段 + UpsertRequest 同步
  internal/agent/tools/message.go                     — 注入 ExpressionSelector（可选）
```

---

## 五、Phase 4：人格记忆增强

### 5.1 目标

在现有记忆系统上增强：
- **用户画像**：自动聚合用户偏好、行为模式
- **多模式检索**：search/time/episode/aggregate/hybrid

### 5.2 现有基础

```
已有实现：
  internal/memory/adapters/provider.go   ← Provider.OnBeforeChat / OnAfterChat / Search
  internal/conversation/flow/resolver_memory.go  ← storeMemory() 在每轮后调用 OnAfterChat
  internal/pipeline/discuss_trigger.go:extractPassiveMemory()  ← 不说话时的被动记忆提取
```

### 5.3 画像服务

作为一个**并行分支**加入 `storeRound`，与 `storeMemory` 同级。

**Provider resolve 优化**：当前 `storeMemory` 每次调用都 resolve 一次 Provider（`resolver_memory.go:27`）。`updateProfile` 也会需要 Provider 来 `Search` 已有画像。优化：在 `storeRound` 中统一 resolve 一次，传入两个 goroutine：

```go
// internal/conversation/flow/resolver_store.go（修改后）

func (r *Resolver) storeRound(ctx context.Context, ...) error {
    // ... existing persistence ...

    // 统一 resolve Provider（避免两个 goroutine 各自 resolve）
    p := r.resolveMemoryProvider(ctx, req.BotID)

    go r.storeMemoryWithProvider(context.WithoutCancel(ctx), req, filtered, p)   // 现有：传入 p
    go r.updateProfile(context.WithoutCancel(ctx), req, filtered, p)             // 新增：复用 p
    return nil
}

// storeMemoryWithProvider 是 storeMemory 的重构版本，接受预解析的 Provider
func (r *Resolver) storeMemoryWithProvider(ctx context.Context, req conversation.ChatRequest,
    messages []conversation.ModelMessage, p adapters.Provider) {
    if p == nil || strings.TrimSpace(req.BotID) == "" {
        return
    }
    // ... 其余逻辑不变 ...
}
```

```go
// internal/memory/profiles/service.go
package profiles

// ProfileService builds and maintains user profiles from memory evidence.
// It is NOT a Provider implementation — it's a standalone service that
// queries the existing memory Provider via Search.
type ProfileService struct {
    memProvider adapters.Provider   // 复用现有记忆查询
    llm         ProfileLLM          // 轻量 LLM 聚合
    cache       ProfileCache        // 内存缓存 + TTL
}

// UpdateFromMessages extracts personality signals from new messages.
// The provider argument allows the caller to supply a pre-resolved Provider
// (avoiding double resolve in storeRound).
func (s *ProfileService) UpdateFromMessages(ctx context.Context, provider adapters.Provider, botID string, messages []adapters.Message) error {
    if provider == nil {
        provider = s.memProvider  // 回退：使用内置 Provider
    }
    // 1. 提取人物性信号（偏好表述、情感倾向、行为模式）
    // 2. 从记忆系统搜索已有画像：provider.Search(ctx, ...)
    // 3. LLM 聚合：新信号 + 旧画像 → 更新画像
    // 4. 存储（作为特殊记忆条目写入 Provider.Add）
}
```

### 5.4 多模式检索

扩展记忆工具的查询参数：

```go
// internal/memory/adapters/types.go

type SearchRequest struct {
    Query     string
    BotID     string
    Limit     int
    Mode      SearchMode  // 新增
    TimeRange *TimeRange  // 新增（mode=time 时使用）
}

type SearchMode string
const (
    ModeSearch    SearchMode = "search"     // 默认：事实性语义搜索
    ModeTime      SearchMode = "time"       // 时间范围
    ModeEpisode   SearchMode = "episode"    // 特定经历/事件
    ModeAggregate SearchMode = "aggregate"  // 整体概况
    ModeHybrid    SearchMode = "hybrid"     // 综合检索
)
```

### 5.5 Settings 扩展

**Settings 新增字段**：

```go
// internal/settings/types.go
type Settings struct {
    // ... existing fields ...
    EnableProfileTracking bool   `json:"enable_profile_tracking"`
    MemorySearchMode      string `json:"memory_search_mode"`  // search/time/episode/aggregate/hybrid
}
```

**UpsertRequest 同步扩展**：

```go
type UpsertRequest struct {
    // ... existing fields ...
    EnableProfileTracking *bool   `json:"enable_profile_tracking,omitempty"`
    MemorySearchMode      *string `json:"memory_search_mode,omitempty"`
}
```

### 5.6 关键修改文件清单

```
新增:
  internal/memory/profiles/service.go        — 画像服务
  internal/memory/profiles/types.go          — Profile, Fact, Relation 类型

修改:
  internal/memory/adapters/types.go          — 增加 SearchMode, TimeRange
  internal/memory/adapters/builtin/          — 支持多模式检索
  internal/conversation/flow/resolver_store.go — 增加 updateProfile() 调用
  internal/settings/types.go                 — 新增 EnableProfileTracking 等字段
```

---

## 六、架构集成总图（修正后）

```
                          ┌─────────────────────────┐
                          │   Channel Adapters       │
                          │ (Telegram/Discord/...)   │
                          └───────────┬─────────────┘
                                      │ messages
                                      ▼
┌─────────────────────────────────────────────────────────────────────┐
│                     pipeline/discuss_trigger.go                     │
│                                                                     │
│  runSession() ─→ debounce ─→ talk_value check                      │
│                      │                                              │
│                      ▼                                              │
│              ┌─────────────────────┐     (已实现)                    │
│              │  chattiming/        │                                │
│              │  timing_gate.go     │                                │
│              │  Evaluate()         │                                │
│              │  → continue/wait/   │                                │
│              │    no_reply         │                                │
│              └──────┬──────────────┘                                │
│                     │                                               │
│          continue   │  no_reply/wait                                │
│                     ▼                                               │
│     handleReplyWithAgent() ─→ StreamChat ─→ storeRound             │
│                                                     │               │
│         ┌───────────────────┬───────────┬───────────┤               │
│         ▼                   ▼           ▼           ▼               │
│   storeMemory        [MessageProvider] [Learner]  [ProfileService]  │
│   (记忆提取,已实现)  (reply工具       (学习,     (画像聚合,          │
│                      LLM重生成,       Phase3)    Phase4)             │
│                      Phase1)                                        │
│         │                   │           │           │               │
│         ▼                   ▼           ▼           ▼               │
│   memory/adapters    *agentpkg.Agent  expression/  Provider.Search   │
│   Provider           (与TimingGate    selector.go  (复用现有接口)     │
│                      同一注入模式)   (向量匹配)                       │
│                                              │                       │
│                                              ▼                       │
│                                     replyer system prompt            │
│                                     (注入匹配的表达风格)              │
└─────────────────────────────────────────────────────────────────────┘
```

### 各 Phase 所属层级

| Phase | 领域层 | 应用层 | 基础设施层 | 接口层 |
|-------|--------|--------|-----------|--------|
| Phase 1: Replyer | — | Resolver (配置注入) | `agent/tools/message.go` (合并于 MessageProvider) | — |
| Phase 2: Timing Gate | `chattiming/TimingGate` | `DiscussTrigger` | `agent` (LLM 调用) | — (已实现) |
| Phase 3: Expression | `expression/{entry,repository,learner,selector}` | `resolver_store` (钩子) + `discuss_trigger` (extractPassiveMemory + DI) | `expression/postgres/` | `agent/tools/jargon.go` + `agent/tools/message.go` (ExpressionSelector 注入) |
| Phase 4: Memory | `memory/profiles/` | `resolver_store` (钩子) | `memory/adapters/builtin/` | — |

> **Phase 1 层级修正**：Replyer 作为 `MessageProvider` 的扩展，不创建独立的基础设施文件。LLM 调用能力通过注入 `*agentpkg.Agent` 获得（与 `TimingGate` 注入 `*Agent` 的模式一致）。

---

## 七、实现顺序与预估（修正后）

| Phase | 依赖 | 工作量 | 预估 | 备注 |
|-------|------|--------|------|------|
| **Phase 2: Timing Gate** | 无 | ~50 行（文档化 + 可选 chat 扩展） | 0.5 天 | **已有完整实现**，仅需确认和配置 |
| **Phase 1: Replyer** | Phase 2 确认 | ~350 行 Go + 1 prompt 文件 | 2-3 天 | ToolProvider 模式，不改 agent.go |
| **Phase 3: Expression/Jargon** | DB 迁移 | ~900 行 Go + SQL | 4-5 天 | 完整 DDD 分层，向量搜索优先 |
| **Phase 4: Memory Enhance** | 现有 memory system | ~500 行 Go | 2-3 天 | 复用现有 hooks 模式 |

**建议执行顺序**：Phase 2（确认现状）→ Phase 1（效果最显著）→ Phase 3（离线学习）→ Phase 4（记忆增强）

---

## 八、关键设计决策（修正后）

### 8.1 Replyer 实现位置

**选择：合并入 `MessageProvider`（零侵入 Agent，零工具冲突）**

| 维度 | 独立 Provider（v2 方案） | 合并入 MessageProvider（v3 方案） |
|------|--------------------------|-----------------------------------|
| agent.go 改动 | **0 行** | **0 行** |
| 工具语义冲突 | ❌ send + reply 同时可见 | ✅ send/reply 互斥，LLM 只看到一个 |
| 代码复用 | ⚠️ send/reply 各自持有 Executor | ✅ 共享 `messaging.Executor` |
| LLM 注入模式 | 需要新接口 | ✅ 与 `TimingGate` (`*agentpkg.Agent`) 完全一致 |
| 测试范围 | 两个 Provider 各自测试 | 一个 MessageProvider 的路径测试 |

### 8.2 Timing Gate 位置

**选择：保留在 DiscussTrigger 层（已实现），可选扩展到 Resolver**

- DiscussTrigger 是 Timing Gate 的**天然所在**：它管理会话生命周期，决定何时触发 LLM
- chat 模式的价值有限（一对一，几乎总是应该回复）
- 不改动 `resolver_stream.go` 的复杂循环

### 8.3 Expression 学习架构

**选择：DDD 分层 + 向量搜索优先**

- `internal/expression/` 作为独立领域包，不混入 `agent/`
- 向量搜索匹配表达方式（复用 Qdrant embedding），无额外 LLM 调用
- 学习钩子挂在 `storeRound` + `extractPassiveMemory` 两个现有路径

### 8.4 画像系统架构

**选择：独立服务，复用 Provider.Search，嵌入 storeRound 并行分支**

- 不修改 Provider 接口
- ProfileService 查询现有记忆来聚合画像
- 作为 `go r.updateProfile(...)` 与 `go r.storeMemory(...)` 并行

### 8.5 特性开关

**选择：通过现有 `settings.Service` 扩展 Settings + UpsertRequest 字段**

每个新特性至少有独立的 bool 开关，且 `UpsertRequest` 同步包含对应的可选字段：

```go
// Settings — 读取侧
type Settings struct {
    // ... existing ...
    EnableReplyer           bool   `json:"enable_replyer"`
    ReplyerModelID          string `json:"replyer_model_id"`
    EnableExpressionLearn   bool   `json:"enable_expression_learning"`
    EnableProfileTracking   bool   `json:"enable_profile_tracking"`
    MemorySearchMode        string `json:"memory_search_mode"`
}

// UpsertRequest — 写入侧（使用指针可选，遵循现有 ReasoningEnabled *bool 模式）
type UpsertRequest struct {
    // ... existing ...
    EnableReplyer           *bool   `json:"enable_replyer,omitempty"`
    ReplyerModelID          string  `json:"replyer_model_id,omitempty"`
    EnableExpressionLearn   *bool   `json:"enable_expression_learning,omitempty"`
    EnableProfileTracking   *bool   `json:"enable_profile_tracking,omitempty"`
    MemorySearchMode        *string `json:"memory_search_mode,omitempty"`
}
```

通过 Bot Settings API 控制，支持前端 UI 配置。

---

## 九、风险与缓解

| 风险 | 影响 | 缓解措施 |
|------|------|---------|
| Replyer 增加每次回复的 LLM 调用（Token 成本） | 中 | 使用低成本模型（GPT-4o-mini 级别），仅 discuss 模式启用，`EnableReplyer` 可控关闭 |
| `send`/`reply` 语义冲突 | 高 | v3 方案：Tools() 互斥返回，LLM 永远不会同时看到两个工具 |
| Expression 学习可能产生噪音数据 | 低 | `checked/rejected` 字段支持人工审核，未审核的不注入 replyer prompt |
| 异步 goroutine 泄漏 | 中 | `TryLock` + `atomic` + `recover` + `context.WithoutCancel`（与现有 `storeMemory` 模式一致），`extractPassiveMemory` 已有 `WithTimeout(2min)` |
| 向量搜索依赖 Qdrant 可用性 | 低 | 回退到关键词搜索 + 缓存；Embedder 独立注入不依赖 Memory Provider |
| Phase 4 画像聚合的准确性 | 低 | 使用 LLM 聚合而非规则，提供用户反馈机制 |
| `DiscussTriggerDeps` 膨胀 | 低 | 新增字段可选（nil 表示未启用），不影响现有逻辑路径 |
| `UpsertRequest` 遗漏导致 API 不可写 | 中 | v3 方案：所有 Settings 新增字段均同步到 UpsertRequest |
