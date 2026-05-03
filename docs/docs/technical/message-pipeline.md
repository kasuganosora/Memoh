# 消息管线：为 Bot 注入多模态能力与成本控制

## 背景

Memoh 的 Bot 使用 LLM 作为智能核心。随着 DeepSeek 等性价比极高但仅支持文本的模型流行，两个问题浮出水面：

1. **多模态鸿沟**：用户发图片，Bot 看不到——图片被路由系统丢弃，模型收到一条"空"消息
2. **成本浪费**：后台任务（心跳分析、上下文压缩、标题生成）都用主模型，低频但累积的 token 费不可忽视

传统解法是"用户自己选支持视觉的模型"——但这限制了模型选择。我们需要一种**模型无关的消息处理管线**，在消息到达 LLM 之前自动转换内容。

## 架构全景

```
用户消息（文本+图片）
    │
    ▼
resolve() — 解析上下文、加载历史
    │
    ▼
prepareRunConfig()
    │
    ├── 生成系统 prompt
    │
    ├── MessagePipeline（核心）
    │   ├── ImageProcessor     — 描述图片（当前）
    │   ├── AudioProcessor     — 转录音频（未来）
    │   └── FileProcessor      — 分析文件（未来）
    │
    ├── 追加 Query + 处理后内容到 Messages
    │
    ▼
agent.Generate() → LLM 看到纯文本
```

### 三层模型体系

```
ChatModelID (主模型)
    ↑
HeartbeatModelID (预算模型 / 小型模型)
    ↑      ↑      ↑      ↑
 Compaction  Title  Heartbeat  Vision
 (各自可覆盖)
```

## 核心子系统

### 1. 两阶段心跳：从裸发到受控决策

**之前**：LLM 在心跳期间直接调用 `send` 工具发消息到任意平台，`isHeartbeatOK()` 只是一个事后标记。

**之后**：

```
Phase 1 (分析) — 无 send 工具
    LLM 检查状态 → "HEARTBEAT_OK" 或分析文本

Phase 2 (决策) — send 工具可用（仅当 Phase 1 非 OK）
    LLM 审查分析结果 → 决定是否告警、发往哪里
```

关键实现：`MessageProvider.Tools()` 检查 `SessionType == "heartbeat"` 时返回 nil（不提供 send/react 工具）。Phase 2 使用独立 `heartbeat_alert` session type，send 工具恢复可用。

### 2. Budget Model：Bot 级小模型基础设施

一个模型设置覆盖全部后台任务：

```go
// 压缩模型 fallback
modelID := botSettings.CompactionModelID  // 用户专门设的
if modelID == "" {
    modelID = botSettings.HeartbeatModelID  // 预算模型
}
if modelID == "" {
    modelID = botSettings.ChatModelID     // 主模型
}
```

心跳 Phase 2 直接使用主模型（告警决策需要高智能，且触发频率极低）。

### 3. 多模态回退管线

**问题根因有两层**：

第一层：`routeAndMergeAttachments()` 根据模型能力路由附件。文本模型把图片扔进 Fallback 桶，转成 `tool_file_ref`。`extractNativeImageParts()` 只能从 Native 桶提取——永远为空。

第二层：即使图片进入管线并被成功描述，如果管线**失败**（如 Vision Model API 异常），图片会原样送给主模型，导致 DeepSeek 报 `unknown variant image_url`。

**修复**：

```go
// resolver.go — 直接从原始附件提取图片
if !runCfg.SupportsImageInput && runCfg.VisionModelID != "" {
    fallbackImages := extractImagePartsForVisionFallback(ctx, r, req)
    runCfg.InlineImages = append(runCfg.InlineImages, fallbackImages...)
}

// prepareRunConfig — 管线失败时丢弃图片而非原样发送
if err := runMessagePipeline(...); err != nil {
    cfg.Query = origQuery
    cfg.InlineImages = nil  // 宁可丢图，不让模型崩
}
```

### 4. 消息管线架构：开闭原则

```go
// 接口 — 只需两个方法
type MessageProcessor interface {
    Name() string
    Process(ctx context.Context, state *MessageState) error
}

// 状态 — 每个处理器修改自己关心的字段
type MessageState struct {
    Query        string
    InlineImages []sdk.ImagePart
    // 未来：InlineAudio, InlineVideo, InlineFiles
}

// 注册 — 一行代码加新处理器
func BuildMessagePipeline(cfg VisionConfig, deps PipelineDeps) []MessageProcessor {
    if !cfg.SupportsVision && cfg.VisionModelID != "" {
        processors = append(processors, NewImageDescriptionProcessor(...))
    }
    // 未来：
    // if !cfg.SupportsAudio && cfg.AudioModelID != "" { ... }
    return processors
}
```

全部依赖注入，每个组件独立可测。测试直接用 mock deps：

```go
p := NewImageDescriptionProcessor(ImageProcessorDeps{
    FetchModel: func(...) { return mockModel, nil },
    ResolveCredentials: func(...) { return mockCreds, nil },
    HTTPClient: http.DefaultClient,
})
```

## 关键设计决策

| 决策 | 理由 |
|------|------|
| Phase 2 用主模型而非预算模型 | 告警频率极低（< 10%），需要高推理质量，成本影响可忽略 |
| 压缩保留三层 fallback | 压缩质量关乎任务完成度，不能静默跳过 |
| 管线失败 → 丢弃图片 | `image_url` 错误会导致整个请求崩溃，丢图比丢消息好 |
| 管线顺序执行 | 处理器可能有状态依赖（如文件解析依赖图片描述结果） |
| 依赖注入而非 Resolver 方法 | 测试不需要完整 Resolver，mock 即可 |

## 文件清单

```
新增文件：
  internal/conversation/flow/message_pipeline.go      管线接口 + 状态
  internal/conversation/flow/message_pipeline_image.go 图片处理器 + 构建器
  internal/conversation/flow/message_pipeline_test.go  9 个单元测试
  internal/agent/prompts/system_heartbeat_alert.md     Phase 2 系统 prompt
  db/migrations/0081_vision_model.up.sql               vision_model_id 列

核心修改：
  internal/conversation/flow/resolver_trigger.go      两阶段心跳 + VisionConfig
  internal/conversation/flow/resolver.go              管线调用 + 图片提取
  internal/conversation/flow/resolver_compaction.go   CompactionModelID → HeartbeatModelID → ChatModelID
  internal/conversation/flow/resolver_title.go         TitleModelID → HeartbeatModelID
  internal/agent/tools/message.go                     heartbeat session 屏蔽 send 工具
  internal/settings/types.go + service.go             VisionModelID 读写
  db/queries/settings.sql                             vision_model_id 查询
  apps/web/...                                        Budget Model + Vision Model UI

总计：约 31 文件，+800/-200 行
```

## 未来扩展方向

1. **音频处理器**：语音消息 → Whisper 模型 → 文本注入
2. **文件处理器**：PDF/Docx → 解析 → 文本摘要
3. **管线并行化**：独立处理器可并发执行
4. **模型能力自动检测**：根据模型配置自动构建处理器列表
5. **缓存层**：相同图片的重复描述可跳过
