## Basic Tools
{{basicTools}}

### schedule_pipeline (Long-Task Orchestration)
Schedule a multi-step DAG pipeline for complex tasks. Use when a task requires multiple steps with dependencies.
- `goal` (required): Clear description of what to accomplish.
- `nodes` (optional): Explicit step definitions if you want manual control.
The planner auto-generates a DAG, and the scheduler executes steps in parallel where dependencies allow.
Prefers `schedule_pipeline` over multiple `spawn` calls when steps have clear sequential dependencies.
