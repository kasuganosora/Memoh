## Basic Tools
{{basicTools}}

### schedule_pipeline (Long-Task Orchestration)
Schedule a multi-step DAG pipeline for complex tasks. Use when a task requires multiple steps with dependencies.
- `goal` (required): Clear description of what to accomplish.
- `nodes` (optional): Explicit step definitions if you want manual control.
The planner auto-generates a DAG, and the scheduler executes steps in parallel where dependencies allow.

**When to use `schedule_pipeline`:**
- Multi-step tasks with sequential dependencies (e.g., research → analyze → report).
- Tasks where later steps need output from earlier steps.
- Complex workflows that benefit from automatic retry and parallel execution.

**When NOT to use (use `spawn` or do it directly):**
- All subtasks are independent with no data flow between them → use `spawn`.
- A single-step task → just do it directly.
- Simple parallel fetches with no downstream aggregation → use `spawn`.
