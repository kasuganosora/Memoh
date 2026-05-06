-- Pipeline CRUD

-- name: CreatePipeline :one
INSERT INTO pipelines (bot_id, goal, status)
VALUES ($1, $2, $3)
RETURNING id, bot_id, goal, status, created_at, updated_at;

-- name: GetPipeline :one
SELECT id, bot_id, goal, status, created_at, updated_at
FROM pipelines
WHERE id = $1;

-- name: ListPipelinesByBot :many
SELECT id, bot_id, goal, status, created_at, updated_at
FROM pipelines
WHERE bot_id = $1
ORDER BY created_at DESC
LIMIT $2 OFFSET $3;

-- name: UpdatePipelineStatus :one
UPDATE pipelines
SET status = $2, updated_at = now()
WHERE id = $1
RETURNING id, bot_id, goal, status, created_at, updated_at;

-- name: DeletePipeline :exec
DELETE FROM pipelines
WHERE id = $1;

-- Pipeline Node CRUD

-- name: CreatePipelineNode :one
INSERT INTO pipeline_nodes (
    pipeline_id, name, description, depends_on, model_tier,
    status, input, max_retries, timeout_seconds, needs_review
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING id, pipeline_id, name, description, depends_on, model_tier,
    status, input, output, error, retry_count, max_retries,
    timeout_seconds, needs_review, review_result, review_feedback,
    created_at, updated_at, started_at, completed_at;

-- name: GetNode :one
SELECT id, pipeline_id, name, description, depends_on, model_tier,
    status, input, output, error, retry_count, max_retries,
    timeout_seconds, needs_review, review_result, review_feedback,
    created_at, updated_at, started_at, completed_at
FROM pipeline_nodes
WHERE id = $1;

-- name: ListNodesByPipeline :many
SELECT id, pipeline_id, name, description, depends_on, model_tier,
    status, input, output, error, retry_count, max_retries,
    timeout_seconds, needs_review, review_result, review_feedback,
    created_at, updated_at, started_at, completed_at
FROM pipeline_nodes
WHERE pipeline_id = $1
ORDER BY created_at ASC;

-- name: UpdateNodeStatus :one
UPDATE pipeline_nodes
SET status = $2,
    started_at = CASE WHEN $2 = 'running' THEN COALESCE(started_at, now()) ELSE started_at END,
    completed_at = CASE WHEN $2 IN ('completed', 'failed', 'cancelled') THEN now() ELSE completed_at END,
    updated_at = now()
WHERE id = $1
RETURNING id, pipeline_id, name, description, depends_on, model_tier,
    status, input, output, error, retry_count, max_retries,
    timeout_seconds, needs_review, review_result, review_feedback,
    created_at, updated_at, started_at, completed_at;

-- name: UpdateNodeOutput :one
UPDATE pipeline_nodes
SET output = $2, status = $3,
    completed_at = CASE WHEN $3 IN ('completed', 'failed') THEN now() ELSE completed_at END,
    updated_at = now()
WHERE id = $1
RETURNING id, pipeline_id, name, description, depends_on, model_tier,
    status, input, output, error, retry_count, max_retries,
    timeout_seconds, needs_review, review_result, review_feedback,
    created_at, updated_at, started_at, completed_at;

-- name: UpdateNodeError :one
UPDATE pipeline_nodes
SET error = $2, status = 'failed', completed_at = now(), updated_at = now()
WHERE id = $1
RETURNING id, pipeline_id, name, description, depends_on, model_tier,
    status, input, output, error, retry_count, max_retries,
    timeout_seconds, needs_review, review_result, review_feedback,
    created_at, updated_at, started_at, completed_at;

-- name: IncrementNodeRetry :one
UPDATE pipeline_nodes
SET retry_count = retry_count + 1, status = 'pending', updated_at = now()
WHERE id = $1
RETURNING id, pipeline_id, name, description, depends_on, model_tier,
    status, input, output, error, retry_count, max_retries,
    timeout_seconds, needs_review, review_result, review_feedback,
    created_at, updated_at, started_at, completed_at;

-- name: UpdateNodeReview :one
UPDATE pipeline_nodes
SET review_result = $2, review_feedback = $3, updated_at = now()
WHERE id = $1
RETURNING id, pipeline_id, name, description, depends_on, model_tier,
    status, input, output, error, retry_count, max_retries,
    timeout_seconds, needs_review, review_result, review_feedback,
    created_at, updated_at, started_at, completed_at;

-- name: DeleteNodesByPipeline :exec
DELETE FROM pipeline_nodes
WHERE pipeline_id = $1;
