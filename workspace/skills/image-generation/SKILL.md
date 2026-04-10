---
name: image-generation
description: Generate images on user request with the native generate_image tool. Offer optional prompt enhancement first, and only use the improved prompt if the user agrees.
metadata:
  openclaw:
    emoji: "🖼️"
---

# Image Generation

Use this skill when the user asks to create, draw, render, illustrate, or generate an image.

## Rules

- Always use the native `generate_image` tool.
- Do not choose a model manually. The tool uses the configured default image-generation model.
- If the user explicitly asks for prompt improvement, help refine it and then use the approved version.
- If the prompt is vague or can clearly benefit from refinement, first offer an improved prompt and wait for consent.
- If the user says no, use the original prompt as-is.
- Do not silently rewrite the prompt before generation.

## Suggested flow

1. Understand the requested image and missing details.
2. If prompt enhancement would help, briefly propose an improved prompt.
3. Wait for the user to confirm the improved prompt.
4. Call `generate_image` with the final approved prompt and any requested options (`size`, `quality`, `n`, `background`, `output_format`).

## Examples

Ask first:

- User: "сгенерируй мрачный постер киберпанк-кота"
- You: "Могу сразу сделать, но хочешь я сначала улучшу промпт и добавлю композицию/свет/стиль?"

Generate after consent:

```json
{
  "prompt": "A dark cyberpunk movie poster featuring a black cat standing in neon rain, dramatic rim lighting, dense futuristic city background, high contrast, cinematic composition",
  "size": "1024x1536",
  "quality": "high",
  "output_format": "png"
}
```
