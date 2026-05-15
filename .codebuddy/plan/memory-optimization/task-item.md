# 实施计划

> 基于 [requirements.md](./requirements.md) 的编码任务清单。任务按依赖顺序排列，底层接口变更优先，上层功能后续跟进。

---

- [ ] 1. 扩展 `BeforeChatResult` 结构体，支持分区上下文返回
   - 修改 `internal/memory/adapters/types.go`，在 `BeforeChatResult` 中新增 `AppendSystemContext string` 和 `PrependUserContext string` 两个字段
   - 保留现有 `ContextText` 字段并添加 `// Deprecated` 注释，确保向后兼容
   - 新增字段零值默认为空字符串，现有 Provider（mem0、openviking）无需改动即可正常工作
   - _需求：1.1、1.6、1.7_

- [ ] 2. 改造 `resolver_memory.go` 消费分区上下文
   - 修改 `internal/conversation/flow/resolver_memory.go` 中的 `loadMemoryContextMessage` 方法
   - 新增 `loadMemorySystemContext` 方法，当 `AppendSystemContext` 非空时返回追加到 system prompt 末尾的内容
   - 兼容逻辑：优先使用新字段 `AppendSystemContext` / `PrependUserContext`；若均为空则回退到旧的 `ContextText` 字段
   - 修改调用 `loadMemoryContextMessage` 的上游代码（`internal/conversation/flow/resolver.go`），将 system context 追加到 system prompt 末尾
   - _需求：1.8、1.6_

- [ ] 3. 实现记忆召回超时保护与熔断机制
   - 在 `internal/memory/adapters/builtin/builtin.go` 的 `OnBeforeChat` 方法中，为 Qdrant 搜索添加 5 秒超时 context
   - 新建 `internal/memory/adapters/builtin/circuit_breaker.go`，实现简单的熔断器：连续 3 次超时后自动跳过 30 秒
   - 修改 `OnBeforeChat` 逻辑：工作记忆搜索与长期记忆搜索分离执行，长期记忆超时时仍返回工作记忆结果（部分降级）
   - 超时和熔断事件记录 warn 级别 slog 日志
   - 编写 `circuit_breaker_test.go` 单元测试，覆盖正常、超时、熔断、恢复四种状态
   - _需求：2.1、2.2、2.3、2.5_

- [ ] 4. 改造 `OnBeforeChat` 输出分区上下文
   - 修改 `internal/memory/adapters/builtin/builtin.go` 的 `OnBeforeChat` 方法
   - 将工作记忆和长期记忆召回结果写入 `PrependUserContext`（动态上下文）
   - 预留 `AppendSystemContext` 的组装位置（Profile 注入和场景索引将在后续任务中填充）
   - 同时保持 `ContextText` 的赋值（向后兼容，值等于 `PrependUserContext`）
   - _需求：1.1、1.4、1.5_

- [ ] 5. 实现 Profile 画像缓存注入到 `AppendSystemContext`
   - 修改 `internal/memory/profiles/service.go`，新增 `GetSummary(ctx, botID, userID) (string, error)` 方法，返回不超过 500 字符的画像摘要
   - 修改 `internal/memory/profiles/cache.go`，为缓存条目增加 5 分钟 TTL 的摘要缓存（复用现有 `memCache` 结构）
   - 在 `BuiltinProvider` 中注入 `ProfileService`，在 `OnBeforeChat` 中调用 `GetSummary`，将结果格式化为 `<user-profile>` 标签写入 `AppendSystemContext`
   - 画像更新时（`updateProfile` 调用后）使缓存失效
   - _需求：6.1、6.2、6.3、6.4_

- [ ] 6. 实现记忆形成 Pipeline 缓冲与 Warm-up 调度
   - 新建 `internal/memory/adapters/builtin/pipeline.go`，实现 `FormationPipeline` 结构体
   - 核心字段：消息缓冲区 `[]adapters.AfterChatRequest`、当前触发阈值、重试计数、空闲定时器
   - 实现 Warm-up 逻辑：新会话前几轮阈值 1→2→4，成熟后降频到 8
   - 实现 `Enqueue(req)` 方法：缓冲消息，达到阈值时批量触发 `runFormation`
   - 实现 `Flush()` 方法：立即处理缓冲区所有消息
   - 实现 5 分钟空闲自动 flush（使用 `time.AfterFunc`）
   - 错误重试：失败时保留消息，最多重试 3 次后丢弃并记录错误日志
   - 编写 `pipeline_test.go` 单元测试
   - _需求：4.1、4.2、4.3、4.4、4.5、4.8_

- [ ] 7. 集成 Pipeline 到 `OnAfterChat` 并支持 flush-on-search
   - 修改 `internal/memory/adapters/builtin/builtin.go` 的 `OnAfterChat`，将消息 enqueue 到 Pipeline 而非直接调用 `runFormation`
   - 修改 `CallTool` 中 `search_memory` 的处理逻辑：搜索前先调用 `pipeline.Flush()`
   - 在 `BuiltinProvider` 中新增 `pipeline *FormationPipeline` 字段，在构造函数中初始化
   - _需求：4.1、4.7_

- [ ] 8. Pipeline 状态持久化与崩溃恢复
   - 新建 `db/migrations/XXXXXX_add_pipeline_state.sql`，创建 `memory_pipeline_state` 表（bot_id, buffer_json, threshold, retry_count, updated_at）
   - 新建 `db/queries/memory_pipeline.sql`，编写 upsert/get/delete 查询
   - 运行 `sqlc generate` 生成 Go 代码
   - 在 `FormationPipeline` 中实现 `Save()` 和 `Restore()` 方法，服务启动时从数据库恢复缓冲状态
   - _需求：4.6_

- [ ] 9. 实现场景聚合层数据模型与存储
   - 新建 `internal/memory/scene/types.go`，定义 `Scene` 结构体（ID, Title, Summary, HeatScore, TimeRange, MemoryIDs, CreatedAt, UpdatedAt）
   - 新建 `internal/memory/scene/store.go`，实现 `SceneStore` 接口及 Qdrant 实现：CRUD 操作，使用 `type: scene` 元数据标记
   - 场景块存储在 Qdrant 中，每个 bot 最多 20 个；超过上限时自动合并热度最低的两个场景
   - 编写 `store_test.go` 单元测试
   - _需求：3.2、3.3、3.7_

- [ ] 10. 在 Dream 周期中集成场景聚合任务
   - 修改 `internal/memory/dream/dream.go`，在 `Run` 方法中新增 Task 4：场景聚合
   - 新建 `internal/memory/dream/scene_aggregation.go`，实现场景聚合逻辑：
     - 获取所有记忆，使用 compact model LLM 将语义相关的记忆聚合为场景块
     - 评估新记忆是否属于已有场景或需要创建新场景
     - 场景块关联记忆被删除/合并时自动更新 memory_ids 和摘要
   - 在 `DreamLLM` 接口中新增 `AggregateScenes(ctx, memories) ([]SceneCandidate, error)` 方法
   - _需求：3.1、3.4、3.8_

- [ ] 11. 场景索引注入与场景搜索集成
   - 修改 `internal/memory/adapters/builtin/builtin.go` 的 `OnBeforeChat`：
     - 生成轻量场景导航索引（标题 + 摘要列表），注入到 `AppendSystemContext`
     - 当用户查询与某场景高度相关时，将场景详细内容注入 `PrependUserContext`
   - 修改 `CallTool` 中 `search_memory` 工具：
     - 新增 `mode` 可选参数（`search` / `time` / `scene`）
     - `mode=time` 时支持 `after` / `before` 参数进行时间范围搜索
     - `mode=scene` 时返回相关场景块及其关联记忆
   - 新增单轮对话内 `search_memory` 调用次数限制（最多 5 次）
   - 更新 `ListTools` 中的工具 schema，添加新参数描述
   - _需求：3.5、3.6、5.1、5.2、5.3、5.4、5.5_
