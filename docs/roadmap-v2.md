# Roadmap：Memoh 增强版（长任务编排 + 类人 Agent）

---

## 一、现有能力（无需开发）

以下能力已经完备，Phase 0 只需确认即可：

| 能力 | 所在位置 | 说明 |
|------|---------|------|
| 容器化 | `internal/workspace/`, `internal/containerd/` | gRPC bridge, 文件版本控制, 生命周期管理 |
| 心跳 Cron | `internal/heartbeat/` | robfig/cron, 默认 30min, 可配置 |
| 定时任务 | `internal/schedule/` | 标准 cron, 时区感知, CRUD 完整 |
| 长期记忆 (Qdrant) | `internal/memory/adapters/builtin/` | 密集+稀疏向量, BM25 |
| 工具调用 | `internal/agent/tools/` | 26 个工具, ToolProvider 接口 |
| SubAgent 并行执行 | `internal/agent/tools/subagent.go` | 并行(池化), 重试(3次+指数退避), 看门狗(3min) |
| 记忆形成 | `internal/memory/adapters/builtin/formation.go` | LLM 提取→候选搜索→决策 ADD/UPDATE/DELETE |
| 消息压缩 | `internal/compaction/` | 后台 goroutine, LLM 总结历史 |
| 对话时序 | `internal/settings/types.go: ChatTimingConfig` | 防抖/门控/中断, 已含 DB 字段 |
| 心跳主动推送 | `internal/heartbeat/ + settings.HeartbeatEnabled` | 配置即用, 仅需更新 Prompt 模板 |
| 内部思考 | `internal/agent/stream.go: EventReasoning*` + discuss 模式 | LLM 推理流事件 + 内心独白输出 |
| 人格文件 | IDENTITY.md + SOUL.md (容器内) | 容器级别, 由 Bot 创建时注入 |
| 用户画像 | `internal/conversation/flow/` ProfileService + ExpressionLearner | 已接入记忆流程 |

### Phase 0 需引入的唯一依赖

**golang-lru**: `hashicorp/golang-lru/v2`

---

## 二、长任务架构总览

```
      用户请求
         │
         ▼
    主 Agent（判断为长任务，使用主模型）
         │
         ├── 调用 schedule_pipeline 工具
         │
         ▼
    Planner 生成 DAG，每个节点标注 model_tier: compact | standard
         │  depends_on 决定依赖，拓扑排序自然决定并行：
         │      例如 A → B, A → C, 则 B 和 C 自动并行
         │      例如 B → D, C → D, 则 D 等 B 和 C 都完才执行
         │
         ▼
    调度器执行 DAG（按 model_tier 路由、并行 fan-out）：
         │
         ├── Worker A（compact）──┐
         │                        ├── 并行执行
         ├── Worker B（compact）──┘
         │         │
         │         ├── 各自完成后 → Reviewer A / Reviewer B（compact，可选）
         │         │
         │         ▼
         ├── Worker C（standard）── 等待 A、B 都完成后才执行
         │         │
         │         ...
         │
         ▼
    最终结果反馈给主 Agent
         │
         ▼
    主 Agent 回复用户
```

- **Planner**：主 Agent 调用 `schedule_pipeline` 时由调度器调用 LLM 生成 DAG
  - 每个节点附带 `model_tier` 标注（LLM 判断：简单任务用 compact，复杂推理用 standard）
  - **并行由 `depends_on` 天然决定**：B.depends_on = [A], C.depends_on = [A] → B 和 C 自动并行。D.depends_on = [B, C] → D 等两者都完成。Planner 只需正确写出依赖关系，调度器拓扑排序自动发现并行批次
- **Worker**：复用 subagent 机制，按 `model_tier` 路由到对应模型执行，接收 `input` 返回 `output`，最大重试 3 次 + 指数退避
- **Reviewer**：可选的 LLM 验证节点，通常用 `compact` 模型对 Worker 输出做 pass/fail/needs_revision 评判

**模型路由**：利用 Memoh 已有的双模型配置——`chat_model_id`（standard）和 `compact_model_id`（compact）

**并行执行原则**：Planner 通过 `depends_on` 表达依赖关系，调度器依据拓扑排序——
- 无 `depends_on` 的节点 = **第一批并行**
- 依赖全部满足的节点 = **下一批并行**
- 天然形成分阶段并行执行，无需手动指定"是否并行"

**记忆隔离原则**：每个 DAG 节点（Planner / Worker / Reviewer）使用独立的内存命名空间，不污染主 Session 的记忆空间：
- 节点执行时创建临时 Session（已复用 subagent 的 `TypeSubagent`），记忆操作限在该命名空间内
- 节点间传递的是 `output` JSON（结构化结果），而非原始对话历史
- 最终只有 Pipeline 整体结果（或主 Agent 选择保留的信息）写回主 Session

---

## 三、开发路线图

### Phase 1：长任务流水线核心（2-3 周）

#### 1.1 DAG 定义与存储

- 新建 `internal/workflow/` 包（⚠️ `internal/pipeline/` 已存在——是 DCP 上下文管线，不用它）
- DAG Go 数据结构：`Pipeline` / `Node` / `Status`（类型文件在 `internal/workflow/types.go`）
- **DB migration**：新建 `db/migrations/NNNN_add_workflow_pipelines.{up,down}.sql`，DDL 如下：
- `<Bot ID>` 级联隔离

```sql
-- up migration
CREATE TABLE pipelines (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    bot_id        UUID NOT NULL REFERENCES bots(id),
    goal          TEXT NOT NULL,
    status        TEXT NOT NULL DEFAULT 'pending',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE pipeline_nodes (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    pipeline_id     UUID NOT NULL REFERENCES pipelines(id) ON DELETE CASCADE,
    name            TEXT NOT NULL,
    description     TEXT,
    depends_on      UUID[] DEFAULT '{}',
    model_tier      TEXT NOT NULL DEFAULT 'standard',  -- compact | standard
    status          TEXT NOT NULL DEFAULT 'pending',
    input           JSONB,
    output          JSONB,
    error           TEXT,
    retry_count     INTEGER NOT NULL DEFAULT 0,
    max_retries     INTEGER NOT NULL DEFAULT 3,
    timeout_seconds INTEGER NOT NULL DEFAULT 300,
    needs_review    BOOLEAN NOT NULL DEFAULT false,
    review_result   TEXT,
    review_feedback TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at      TIMESTAMPTZ,
    completed_at    TIMESTAMPTZ
);
```

- **sqlc 查询**：新建 `db/queries/workflow.sql`，写 CRUD + 状态更新查询，运行 `mise run sqlc-generate`

#### 1.2 调度器

- 新建 `internal/workflow/scheduler.go`，`Scheduler` 结构体
- **不引入 NATS/消息队列**，调度器内嵌在服务中
- **回调驱动**（非轮询）：节点执行完成 → 回调检查依赖 → 拓扑排序 → 调度就绪节点
- 节点执行复用 `agent.Generate()`，不另建 Worker 接口
- 拓扑排序 + 循环依赖检测 + **并行 fan-out**（同一批无依赖节点并发执行）

**执行流程**：
```
schedule_pipeline 工具被调用
       │
       ▼
  调度器调用 LLM（Planner），生成 DAG → 写入 DB
       │   Planner 输出 depends_on 决定依赖关系
       │   无交叉依赖的节点 = 可并行
       ▼
  Resume() 解析 DAG，拓扑排序 → 找到第一批就绪节点（depends_on 为空）
       │
       ▼
  并行 fan-out → 每个就绪节点 spawn subagent（并发数受 subagent 的 maxSpawnConcurrency=2 约束）
       │
       ├─ 全部成功 → 写 output → 回调 Resume() → 依赖刚完成的节点全部就绪 → 下一批并行
       ├─ 部分失败 → 重试 (max_retries, 指数退避) → 仍失败 → 标记 FAILED → 通知主 Agent
       └─ needs_review 节点 → compact 模型 review (pass/fail/needs_revision)
                                → pass → 视为成功 / fail → 重试
```

> **Planner 即调度器的 LLM 调用**：调度器调用 `agent.Generate()` 传入规划专用 prompt（如 `system_schedule.md` 变体），LLM 输出 DAG 结构的 JSON，调度器解析后写入 DB。Planner 不是独立服务。

#### 1.3 节点执行与模型路由

- **model_tier = compact**：使用 `compact_model_id`（如 gpt-4o-mini, llama3-8b） — 搜索、文件读写、格式转换、Review 判决等简单操作
- **model_tier = standard**（默认）：使用 `chat_model_id` — 复杂推理、代码生成、多步分析
- Reviewer 节点：固定使用 `compact` 模型做 pass/fail 判决
- 所有 Worker 复用 subagent 机制（`system_subagent.md` prompt）
- 工具节点可跳过 LLM 直接执行（如 `web_search`, `send_message`）

#### 1.4 主 Agent 触发

- 新建 `internal/agent/tools/pipeline.go`，实现 `PipelineProvider`（遵循 `ToolProvider` 接口）
- 工具名：`schedule_pipeline`，参数：`goal`（必需），`nodes?`（可选，用户显式指定节点）
- 工具执行时：调用 Scheduler → 规划 DAG → 写入 DB → 异步开始执行
- System Prompt（`_tools.md`）增加 Few-shot 示例：判断长任务 → 使用此工具
- 支持预定义 pipeline 模板（搜索→总结→发送、研究→编码→测试）

#### 1.5 FX 依赖注入

- 在 `cmd/agent/module.go` 中注册：
  - `workflow.Scheduler` 作为单例（依赖 `*agent.Agent`, `*settings.Service`, `*sqlc.Queries`）
  - `tools.PipelineProvider` 作为新的 ToolProvider（注入到 Agent 的 `SetToolProviders`）
- HTTP handler：新建 `internal/handlers/pipeline.go`，遵循 `PipelineHandler` struct + `Register(e *echo.Echo)` 模式

**交付**：可执行多步骤 DAG 流水线，主 Agent 可触发。

---

### Phase 2：流水线健壮性（1 周）

#### 2.1 超时控制

- 复用 subagent 已有的看门狗（`subagentWatchdogTimeout = 3min`）
- `timeout_seconds` 通过 `context.WithTimeout` 传递
- 超时后标记节点 `FAILED`，触发重试

#### 2.2 失败重试

- 复用 subagent 原有的 `subagentMaxRetries = 3` + `subagentRetryBaseDelay`
- 所有重试耗尽后标记 `FAILED`，保留 `error` 信息

#### 2.3 可观测性 API

- 新建 `internal/handlers/pipeline.go`，`PipelineHandler` 注册路由：
  - `GET /bots/:bot_id/pipelines/:id/status` → DAG 完整状态 JSON
  - `GET /bots/:bot_id/pipelines/:id/graph` → 拓扑结构（供前端绘图）
  - `POST /bots/:bot_id/pipelines/:id/retry` → 重试失败节点
- 前端：Vue 组件显示 DAG 进度图

**不实现**：死信队列、熔断器（重试已足够）。

**交付**：流水线具备超时、重试、可监控能力。

---

### Phase 3：分层记忆与 Dream（2-3 周）

#### 3.1 工作记忆 LRU 缓存

- 新建 `internal/memory/working/` 包
- **Bot 级别** LRU 缓存（`hashicorp/golang-lru/v2`），容量 1000
- 结构：

```go
type WorkingMemory struct {
    lru    *lru.Cache[string, *MemoryEntry]
}

type MemoryEntry struct {
    Content     string         `json:"content"`
    Importance  string         `json:"importance"`   // high / medium / low
    AccessCount int            `json:"access_count"`
    CreatedAt   time.Time      `json:"created_at"`
    LastAccess  time.Time      `json:"last_access_at"`
    Metadata    map[string]any `json:"metadata,omitempty"`
}
```

- **注入点**：WorkingMemory 注入到内置 memory provider 的 `context_packer.go` 中
  - `OnBeforeChat` 时：从 LRU 检索命中信息，追加到 `BeforeChatResult.ContextText`（跨 Session 短期连续性）
  - `OnAfterChat` 时：将本轮形成的记忆同步到 LRU
  - 注意：`BeforeChatResult.ContextText` 追加到 `_memory.md` 模板区域

#### 3.2 重要性标签

- 不新建独立工具
- 扩展 `formation.go` 中的 LLM `Decide` 指令，要求输出 `importance`
- 元数据中保留 `importance` 字段

#### 3.3 LRU → Qdrant 升级

- **触发时机**：内置 provider 的 `OnAfterChat` 钩子中，每次对话结束后扫描 LRU
- LRU 中 `access_count >= 3 && importance != low` → 调用 provider 的 `Add()` 写入 Qdrant
- 写入时保留元数据：`{source:"working_memory", promoted_at:"...", access_count:N}`

#### 3.4 Dream 后台任务

- 新建 `internal/memory/dream/` 包
- 利用现有 `schedule` 服务（非新 cron）配置触发（默认每天凌晨 2 点）
- **与现有 compaction 的关系**：
  - `compaction` 压缩**会话消息上下文**（短期的对话历史），避免 Token 超限
  - `dream` 整理/合并/清理**Qdrant 长期记忆**（跨 Session 沉淀的事实）
  - 两者互补，分别在各自的维度维护记忆健康

**任务一：合并相似记忆**
- 从 Qdrant 搜索候选，LLM 判断相似度 > 0.9 的可合并
- 保留合并引用

**任务二：标记有害/过时记忆**
- 搜索有效记忆，LLM 判断是否被用户纠正或含隐私关键词
- 标记 `deleted = true`

**任务三：记忆关联强化**（V2，本次不做）

**交付**：Agent 有"短期记住→重复遇见→永久记住"的分层行为，夜间整理记忆。

---

### Phase 4：行为拟真（0.5 周，Prompt 工程为主）

#### 4.1 对话节奏

- `ChatTimingConfig` 已完整实现于 `internal/settings/types.go`
- 前端应用已存在的 timing 字段展示打字效果
- 无后端改动

#### 4.2 动作描述

- 更新 `system_chat.md`：提示 LLM 适当输出 `[...]` 动作标记
- 前端匹配渲染为斜体

#### 4.3 心跳主动推送

- 更新 `heartbeat.md` / `system_heartbeat.md`：加入问候、提醒、记忆关联示例
- 功能本身已完整

#### 4.4 人格一致性增强

- **DB 迁移**：新建 migration，bots 表增加 `persona` JSONB 列
- **sqlc**：更新 `db/queries/settings.sql`，运行 `mise run sqlc-generate`
- Settings API（`internal/settings/service.go`）：在 `UpsertBot` 中增加 `persona` 字段的读-合并-写逻辑
- Bot settings 增加可编辑 `persona` JSON 字段（覆盖容器默认 IDENTITY.md/SOUL.md）
- 长期记忆检索时附加"过去对此话题的表态"上下文

#### 4.5 内部思考

- ✅ 已实现（LLM 推理流 `EventReasoning*` + discuss 内心独白）
- 可选：调试 API 透传 reasoning 内容

---

## 四、项目估计

| 阶段 | 时间 | 工作内容 |
|------|------|---------|
| Phase 0 准备 | **0.5 周** | 现有能力确认 + 引入 golang-lru |
| Phase 1 流水线核心 | **2-3 周** | DAG 定义、调度器、节点执行、主 Agent 集成 |
| Phase 2 健壮性 | **1 周** | 超时、重试、API + 前端进度图 |
| Phase 3 分层记忆 | **2-3 周** | LRU、importance 标签、升级逻辑、Dream |
| Phase 4 行为拟真 | **0.5 周** | Prompt 工程为主，少量设置字段扩展 + DB 迁移 |
| **合计** | **6-9 周** | |

## 五、不做事项（节省时间 > 成本）

| 初稿计划事项 | 原因 |
|-------------|------|
| 引入 NATS | 不存在，subagent + 内存总线已够用 |
| SQLite 存储 | 主库是 PostgreSQL，统一管理 |
| 死信队列 (DLQ) | 简化失败处理，保留 error 字段即可 |
| 熔断器 | subagent 重试已处理偶发失败 |
| 独立 `classify_importance` 工具 | 现有 formation 可扩展 |
| Internal Thought 开发 | 已实现（推理流 + discuss 模式） |
| 消息队列分发任务 | 调度器内嵌，事件驱动 |
