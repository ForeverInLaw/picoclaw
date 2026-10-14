# Context budgeting

The agent estimates the complete provider request: system instructions and context contributors, conversation history, tool schemas, media, and the configured `max_tokens` answer reserve. Proactive compression, the final check before every provider attempt, and the asynchronous summary threshold use the same estimate. Cache-aware `SystemParts` and their plain-text `Content` fallback are alternative representations; the larger representation is counted once.

This is a heuristic, not a model tokenizer. Text uses approximately 2.5 characters per token, message and tool structures have estimated overhead, and each image has a fixed allowance. Actual provider tokenization and image costs can differ, so context-limit errors still use the existing bounded compression/retry path.

The final check runs after tool results, steering messages, and LLM hooks have assembled the request. It can omit older complete turns from that request while retaining system contributors and the active turn, including its attachments and completed tool results. If those retained messages and the answer reserve exceed `context_window`, the turn reports a configuration/input error before another provider call. Emergency trimming after a provider context-limit error also protects the active turn, so completed actions remain visible to the model during recovery.

Background summaries compare the complete next-request estimate with `context_window * summarize_token_percent / 100`; `summarize_message_threshold` remains an independent trigger. Summaries stay scoped to the owning conversation and keep the existing Conversation reset fence.

Media remains stored as reusable references in conversation history. Every initial or rebuilt provider request resolves images to data URLs and documents to local path tags, including a rebuild after a context-limit error. Inline conversation privacy, reply routing, answer classification, and model retry limits keep their existing behavior.

Selectable Seahorse context management uses the same shared estimator and request checks. It compacts only historical source context, retaining the active turn's text, media and tool sequence during a retry. See [scoped active context](seahorse-context.md) for engine selection and lifecycle limits.
