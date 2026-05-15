# Memoh Memory 模块优化需求文档

## 引言

本文档基于对 TencentDB-Agent-Memory（腾讯开源的 Agent 记忆系统）的深入分析，结合 Memoh 当前 memory 模块和上下文管理模块的现状，提出一系列优化需求。

### 背景

**TencentDB-Agent-Memory 的核心优势：**
- **L0→L1→L2→L3 四层金字塔记忆架构**：从原始对话到原子事实、场景聚合、用户画像，层层抽象
- **Context Offload 机制**：将冗长的工具调用日志 offload 到外部文件，上下文中只保留 Mermaid 状态图摘要
- **上下文分区注入**：稳定部分（persona/场景索引）放 system prompt 尾部可缓存，动态部分放 user prompt 前
- **Pipeline 流水线 + Warm-up 机制**：消息缓冲 + 指数递增触发阈值，避免每轮对话都触发记忆提取
- **记忆可追溯性**：Persona→Scenario→Atom→Conversation 完整链路

**Memoh 当前架构：**
- 扁平向量存储（Qdrant）+ LRU 工作记忆缓存
- Extract→Decide 两步记忆形成流程（每轮对话同步触发）
- 统一的 `<memory-context>` 标签注入到 user message
- Dream 维护（合并/清理/关联）+ Profile 提取
- Compaction 对话摘要压缩
- storefs 文件系统持久化（Markdown 格式的每日记忆文件）

### 优化目标

在保持 Memoh 现有架构稳定性的前提下，借鉴 TencentDB-Agent-Memory 的优秀设计，提升以下方面：
1. **记忆结构化**：引入场景聚合层，让零散记忆形成有组织的知识结构
2. **上下文效率**：分区注入 + prompt cache 友好，降低 token 成本
3. **记忆形成效率**：Pipeline 流水线化 + Warm-up，降低 LLM 调用频率
4. **系统健壮性**：超时保护、优雅降级、崩溃恢复

---

## 需求

### 需求 1：上下文分区注入与 Prompt Cache 优化

**用户故事：** 作为一名 Memoh 运营者，我希望记忆上下文能够分区注入（稳定部分放 system prompt、动态部分放 user prompt），以便利用 LLM 的 prompt cache 机制降低 30-50% 的 token 成本。

#### 验收标准

1. WHEN `OnBeforeChat` 被调用 THEN 系统 SHALL 返回包含两个字段的结果：`AppendSystemContext`（稳定上下文，追加到 system prompt 尾部）和 `PrependUserContext`（动态上下文，注入到 user prompt 前）。
2. IF 用户画像（Profile/Persona）已存在 THEN 系统 SHALL 将用户画像摘要放入 `AppendSystemContext`，因为画像在会话内不会频繁变化，适合 prompt cache。
3. IF 场景导航索引已存在（需求 3 实现后）THEN 系统 SHALL 将场景索引放入 `AppendSystemContext`。
4. WHEN 本轮召回的相关记忆条目生成后 THEN 系统 SHALL 将其放入 `PrependUserContext`，因为每轮查询不同，内容动态变化。
5. WHEN 工作记忆（Working Memory）条目被召回 THEN 系统 SHALL 将其放入 `PrependUserContext`。
6. IF `AppendSystemContext` 为空 THEN 系统 SHALL 不修改 system prompt，保持向后兼容。
7. WHEN `BeforeChatResult` 结构体变更后 THEN 所有现有的 Provider 实现（mem0、openviking）SHALL 继续正常工作，新增字段默认为空。
8. WHEN `resolver_memory.go` 中的 `loadMemoryContextMessage` 处理新结构时 THEN 系统 SHALL 将 `AppendSystemContext` 追加到 system prompt 末尾，将 `PrependUserContext` 作为 user message 注入（与当前 `ContextText` 行为一致）。

---

### 需求 2：记忆召回超时保护与优雅降级

**用户故事：** 作为一名 Memoh 用户，我希望即使记忆服务响应缓慢或不可用时，对话也不会被阻塞，以便获得流畅的对话体验。

#### 验收标准

1. WHEN `OnBeforeChat` 执行记忆搜索时 THEN 系统 SHALL 使用 5 秒超时的 context，超时后跳过记忆注入而不阻塞对话。
2. WHEN Qdrant 搜索超时 THEN 系统 SHALL 记录 warn 级别日志并返回空的 `BeforeChatResult`（而非错误）。
3. WHEN 工作记忆搜索正常但长期记忆搜索超时 THEN 系统 SHALL 仍然返回工作记忆的结果，实现部分降级。
4. WHEN `OnAfterChat` 执行记忆形成时 THEN 系统 SHALL 使用独立的超时控制（当前已有 60s `formationTimeout`），不影响对话响应。
5. IF 连续 3 次记忆搜索超时 THEN 系统 SHALL 在后续 30 秒内自动跳过记忆搜索（熔断机制），避免持续的超时等待。

---

### 需求 3：场景聚合层（L2 Scene Layer）

**用户故事：** 作为一名 Memoh 用户，我希望我的零散记忆能被自动组织成有意义的"场景"（如"旅行计划"、"工作项目"、"学习笔记"），以便 Agent 能更好地理解我的生活上下文并提供更精准的回忆。

#### 验收标准

1. WHEN Dream 维护周期运行时 THEN 系统 SHALL 新增一个"场景聚合"任务，将语义相关的原子记忆聚合为场景块。
2. WHEN 场景块被创建时 THEN 每个场景块 SHALL 包含：标题（title）、摘要（summary）、热度分数（heat score）、时间范围（time range）、关联记忆 ID 列表（memory_ids）。
3. WHEN 场景块数量超过上限（默认 20 个）THEN 系统 SHALL 自动合并热度最低的两个场景块为一个。
4. WHEN 新的记忆条目被添加时 THEN 系统 SHALL 在下一次 Dream 周期中评估该记忆是否属于已有场景，或需要创建新场景。
5. WHEN `OnBeforeChat` 执行时 THEN 系统 SHALL 生成一个轻量的场景导航索引（标题 + 摘要列表），注入到 `AppendSystemContext` 中。
6. IF 用户查询与某个场景高度相关 THEN 系统 SHALL 在 `PrependUserContext` 中注入该场景的详细内容（包含关联记忆）。
7. WHEN 场景块被持久化时 THEN 系统 SHALL 将其存储在 Qdrant 中（带 `type: scene` 元数据标记），同时通过 storefs 同步为 Markdown 文件（`memory/scenes/` 目录）。
8. WHEN 场景块的关联记忆被删除或合并时 THEN 系统 SHALL 自动更新场景块的 memory_ids 和摘要。

---

### 需求 4：记忆形成 Pipeline 流水线化与 Warm-up 机制

**用户故事：** 作为一名 Memoh 运营者，我希望记忆形成过程能够智能调度（而非每轮对话都触发），以便降低 LLM 调用成本并提升系统吞吐量。

#### 验收标准

1. WHEN 新的对话消息到达 `OnAfterChat` 时 THEN 系统 SHALL 将消息缓冲到 Pipeline 中，而非立即触发 Extract→Decide 流程。
2. WHEN 缓冲消息数量达到触发阈值时 THEN 系统 SHALL 批量执行 Extract→Decide→Apply 流程。
3. WHEN 新会话开始时 THEN 系统 SHALL 使用 Warm-up 模式：前几轮快速触发（阈值 1→2→4），成熟后降频到默认阈值（如 8 轮）。
4. WHEN 会话空闲超过 5 分钟 THEN 系统 SHALL 自动 flush 缓冲区中的待处理消息，确保记忆不丢失。
5. IF Pipeline 处理过程中发生错误 THEN 系统 SHALL 保留缓冲消息并在下次触发时重试，最多重试 3 次后丢弃并记录错误日志。
6. WHEN Pipeline 状态需要持久化时 THEN 系统 SHALL 将当前缓冲状态和触发阈值存储到数据库，支持服务重启后恢复。
7. WHEN 用户通过 `search_memory` 工具主动搜索记忆时 THEN 系统 SHALL 立即 flush 当前缓冲区，确保最新对话内容可被搜索到。
8. WHEN 批量 Extract 执行时 THEN 系统 SHALL 将多轮对话消息合并为一个 Extract 请求，减少 LLM 调用次数。

---

### 需求 5：记忆搜索工具增强

**用户故事：** 作为一名 Memoh 用户，我希望 Agent 能够更灵活地搜索我的记忆（包括按时间范围、按场景、按对话历史），以便更精准地回忆过去的信息。

#### 验收标准

1. WHEN Agent 调用 `search_memory` 工具时 THEN 系统 SHALL 支持可选的 `mode` 参数，包括：`search`（默认语义搜索）、`time`（时间范围搜索）、`scene`（场景搜索）。
2. WHEN `mode` 为 `time` 时 THEN 系统 SHALL 支持 `after` 和 `before` 参数（ISO 8601 格式），返回指定时间范围内的记忆。
3. WHEN `mode` 为 `scene` 时 THEN 系统 SHALL 返回与查询最相关的场景块及其关联记忆。
4. WHEN Agent 在单轮对话中多次调用 `search_memory` 时 THEN 系统 SHALL 限制最多 5 次调用，超出后返回友好提示。
5. WHEN `search_memory` 执行时 THEN 系统 SHALL 使用 5 秒超时，超时后返回已获取的部分结果（而非错误）。

---

### 需求 6：Profile 画像注入优化

**用户故事：** 作为一名 Memoh 用户，我希望 Agent 能够在每次对话中自然地利用我的用户画像信息，以便提供更个性化的回复。

#### 验收标准

1. WHEN 用户画像存在且非空时 THEN 系统 SHALL 将画像摘要（核心特征 + 关键事实）格式化为 `<user-profile>` 标签，注入到 `AppendSystemContext` 中。
2. WHEN 画像摘要被注入时 THEN 系统 SHALL 控制摘要长度不超过 500 字符，避免占用过多 system prompt 空间。
3. IF 画像在本次会话中已被注入 THEN 系统 SHALL 在后续轮次中复用缓存的画像摘要（5 分钟 TTL），避免重复查询。
4. WHEN 画像更新时 THEN 系统 SHALL 使缓存失效，确保下次对话使用最新画像。

---

## 技术约束与边界条件

### 向后兼容性
- `BeforeChatResult` 结构体的变更必须向后兼容：新增字段使用零值默认，现有的 `ContextText` 字段保留但标记为 deprecated
- 所有 Provider 实现（mem0、openviking）无需立即适配新字段，使用旧字段时行为不变
- 现有的 `search_memory` 工具接口保持兼容，新参数均为可选

### 性能约束
- `OnBeforeChat` 总耗时不超过 5 秒（含所有记忆搜索和上下文组装）
- Pipeline 缓冲区内存占用不超过 10MB per bot
- 场景聚合 LLM 调用使用 compact model，单次调用不超过 2000 tokens

### 存储约束
- 场景块存储在 Qdrant 中，每个 bot 最多 20 个场景块
- Pipeline 状态持久化到 PostgreSQL，使用现有的 sqlc 查询框架
- storefs 场景文件存储在 `memory/scenes/` 目录下

### 可观测性
- 所有新增模块必须使用 `slog` 结构化日志
- 关键指标（Pipeline 缓冲大小、场景数量、超时次数、熔断状态）通过日志输出
