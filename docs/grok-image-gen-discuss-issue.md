---
name: Grok image generation not working in discuss mode
description: Grok 4 Fast receives generate_image tool but never calls it in discuss mode — all attempted fixes and their outcomes
type: project
---

## Problem

Bot on Misskey timeline needs to generate images via `generate_image` tool. The tool is registered and passed to the LLM, but Grok 4 Fast (openai-responses client) consistently responds with text descriptions instead of calling the tool.

**Why:** LLM behavioral issue — Grok 4 Fast chooses to describe images in text rather than invoking the `generate_image` tool, despite explicit prompt instructions.

## Key Findings (confirmed via debug logs)

1. **Tool IS registered**: `assembleTools` collects 29 tools including `generate_image`
2. **Tool IS passed to LLM**: `buildGenerateOptions` confirms 8 tools after whitelist filtering, `supports_tool_call=true`, `model_id=grok-4-fast-reasoning`
3. **Whitelist includes `generate_image`**: Misskey channel `allowed_tools` has it
4. **LLM simply doesn't call it**: Grok 4 Fast responds with text like "为您绘制的虎皮鹦鹉海报" without any tool call
5. **User confirmed it worked before in web view (chat mode)**: "之前在web视图里是可以画图的"

## Technical Context

- **Chat model**: `grok-4-fast-reasoning` via `openai-responses` client type, provider xAI
- **Image model**: `grok-imagine-image-pro` via `openai-images` client type, has `image-output` + `image-api` compatibilities
- **Bot ID**: `8d956f15-dd25-4086-9908-ea96cd96649d`
- **xAI DOES support Responses API** (`/v1/responses`) — confirmed from docs.x.ai
- **Tool call format is correct**: `{"type": "function", "name": "...", "parameters": {...}}` — matches xAI docs

## Chat mode vs Discuss mode differences

| Aspect | Chat mode (web view) | Discuss mode (Misskey) |
|--------|---------------------|----------------------|
| Agent method | `Agent.Generate()` | `Agent.Stream()` |
| Tool assembly | Same `assembleTools()` | Same `assembleTools()` |
| Tool whitelist | Telegram whitelist | Misskey whitelist |
| Late binding prompt | No | Yes (user msg appended before LLM call) |
| Prompt tells LLM to use `send` tool | No (text rendered directly) | Yes ("You MUST use the send tool") |

## Attempted Fixes (all failed)

1. **Enhanced tool description**: Added "MUST call this tool" language → no change
2. **Late-binding prompt rules**: Added "Tool Usage Rules" section telling LLM to call `generate_image` → no change
3. **`send_instruction` in return value**: Added instruction in tool return value → irrelevant since tool never called
4. **Auto-detect + post-process (implemented but reverted)**: Detect image descriptions in LLM output and auto-trigger image generation — user felt this approach was wrong

## Unresolved Questions

- Why does it work in chat mode (web view) but not discuss mode? Both use same tool assembly path.
- Does Grok behave differently when `send` tool instructions are in the same prompt as `generate_image`?
- Would a different model (e.g. Claude, GPT-4o) call the tool correctly?
- Would `tool_choice: "required"` or per-tool `tool_choice` help for image requests?

## How to apply

When revisiting this issue, consider:
- Testing with a different chat model first to confirm it's Grok-specific
- Checking if the `send` tool instruction in late-binding prompt confuses Grok's tool selection
- Investigating if chat mode worked because the user's prompt was more direct ("draw X") vs discuss mode's wrapped context
