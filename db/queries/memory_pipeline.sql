-- name: UpsertMemoryPipelineState :exec
INSERT INTO memory_pipeline_state (bot_id, buffer_json, threshold, warmup_index, retry_count, updated_at)
VALUES (@bot_id, @buffer_json, @threshold, @warmup_index, @retry_count, NOW())
ON CONFLICT (bot_id) DO UPDATE SET
    buffer_json = EXCLUDED.buffer_json,
    threshold = EXCLUDED.threshold,
    warmup_index = EXCLUDED.warmup_index,
    retry_count = EXCLUDED.retry_count,
    updated_at = NOW();

-- name: GetMemoryPipelineState :one
SELECT bot_id, buffer_json, threshold, warmup_index, retry_count, updated_at
FROM memory_pipeline_state
WHERE bot_id = @bot_id;

-- name: DeleteMemoryPipelineState :exec
DELETE FROM memory_pipeline_state
WHERE bot_id = @bot_id;

-- name: ListMemoryPipelineStates :many
SELECT bot_id, buffer_json, threshold, warmup_index, retry_count, updated_at
FROM memory_pipeline_state
ORDER BY updated_at DESC;
