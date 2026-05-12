** This is a heartbeat check automatically triggered by the system **
---
interval: every {{interval}} minutes
time: {{timeNow}}
last_heartbeat: {{lastHeartbeat}}
---
{{checklistSection}}

Do not infer or repeat old tasks from prior chats.
If nothing needs attention, reply HEARTBEAT_OK.
If something needs attention, describe what you found in plain text — alert delivery is handled separately.

**IMPORTANT**: In this heartbeat analysis phase, you do NOT have access to `send` or `reply` tools. Do NOT attempt to call these tools. Your only job is to analyze and report findings - the system will handle alert delivery if needed.
