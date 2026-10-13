# Telegram experience specification

Published as [IVO-80](https://linear.app/ivoxygen/issue/IVO-80/feattelegram-rich-replies-inline-memory-tool-activity-and-search), marked `ready-for-agent`.

Implementation tickets (blocking relationships are also set natively in Linear):

| Issue | Slice | Blocked by |
| --- | --- | --- |
| [IVO-81](https://linear.app/ivoxygen/issue/IVO-81/fixchannels-keep-progress-separate-from-final-replies) | fix(channels): keep progress separate from final replies | None |
| [IVO-82](https://linear.app/ivoxygen/issue/IVO-82/fixagent-keep-reasoning-and-tool-commentary-out-of-answers) | fix(agent): keep reasoning and tool commentary out of answers | None |
| [IVO-83](https://linear.app/ivoxygen/issue/IVO-83/feattelegram-send-rich-replies-with-native-latex) | feat(telegram): send rich replies with native LaTeX | None |
| [IVO-84](https://linear.app/ivoxygen/issue/IVO-84/fixagent-retry-transient-failures-in-direct-and-inline-requests) | fix(agent): retry transient failures in direct and inline requests | IVO-81 |
| [IVO-85](https://linear.app/ivoxygen/issue/IVO-85/feattelegram-stream-rich-answers-with-reliable-final-delivery) | feat(telegram): stream rich answers with reliable final delivery | IVO-81, IVO-82, IVO-83 |
| [IVO-86](https://linear.app/ivoxygen/issue/IVO-86/feattelegram-show-each-tool-action-and-its-result) | feat(telegram): show each tool action and its result | IVO-81, IVO-83 |
| [IVO-87](https://linear.app/ivoxygen/issue/IVO-87/fixmemory-start-a-fresh-conversation-after-reset) | fix(memory): start a fresh conversation after reset | None |
| [IVO-88](https://linear.app/ivoxygen/issue/IVO-88/feattelegram-persist-isolated-inline-conversations-with-reset) | feat(telegram): persist isolated inline conversations with reset | IVO-87 |
| [IVO-89](https://linear.app/ivoxygen/issue/IVO-89/featsearch-add-parallel-to-web-search) | feat(search): add Parallel to web search | None |
| [IVO-90](https://linear.app/ivoxygen/issue/IVO-90/featsearch-add-exa-with-configurable-provider-fallback) | feat(search): add Exa with configurable provider fallback | IVO-89 |

## Problem Statement

Telegram users cannot reliably tell what the assistant is doing during a long request. Tool calls and their output are inconsistently visible, intermediate model text can appear and disappear in the answer, and an empty final answer can expose reasoning. Temporary API errors are handled differently across providers and delivery paths.

Inline calls currently do not maintain a separate conversation history. A conversation reset clears the current history and summary but does not establish a durable boundary against other automatically supplied context. Telegram's newer rich formatting and native formulas are also unavailable through the currently pinned client. Users want both Parallel and Exa available for web search.

## Solution

Provide rich Telegram replies with native inline and display LaTeX formulas, a persistent and isolated Inline conversation per Telegram user, and one Tool activity message for every tool invocation in direct chats and groups. Each activity message starts with the action and is updated with its result or error.

Make Conversation reset mean a new conversation: earlier dialogs, summaries, and temporary notes stop entering context automatically, while Saved facts remain and archived dialogs can be searched explicitly. Keep reasoning out of the user-facing answer and apply bounded retries to temporary failures in direct and inline conversations.

Add Parallel and Exa behind the existing web-search tool, with a configurable primary provider and transient-error fallback. Deliver this incrementally on a feature branch based on dev.

## User Stories

1. As a Telegram user, I want rich replies with headings, so that long answers are easy to navigate.
2. As a Telegram user, I want native tables and lists, so that structured information remains readable.
3. As a Telegram user, I want code blocks to preserve their formatting, so that commands and examples are usable.
4. As a Telegram user, I want LaTeX formulas within sentences, so that mathematical explanations are readable.
5. As a Telegram user, I want separate display formulas, so that larger mathematical expressions are easy to inspect.
6. As a direct-chat user, I want rich answer drafts while generation continues, so that I can see useful progress.
7. As a group user, I want rich final replies, so that shared answers have the same formatting quality as direct replies.
8. As an inline user, I want rich answers in the destination chat, so that published replies retain their structure and formulas.
9. As a Telegram user, I want long replies delivered within Telegram's limits, so that content is not silently lost.
10. As a Telegram user, I want replies to retain their message and topic association, so that answers appear in the correct discussion.
11. As a Telegram user, I want a readable fallback when formatting fails, so that I still receive the answer.
12. As an inline user, I want follow-up calls to remember earlier selected calls, so that I can continue a conversation.
13. As an inline user, I want my Inline conversation to continue across destination chats, so that I do not have to restate the context.
14. As an inline user, I want history to survive a bot restart, so that ongoing work is not lost.
15. As an inline user, I want my inline history isolated from other users, so that their conversations never affect my replies.
16. As an inline user, I want my inline history isolated from my direct and group conversations, so that published answers do not draw on their private context.
17. As an inline user, I want typing and abandoned inline queries excluded from conversation history, so that only selected calls become part of the dialog.
18. As an inline user, I want long history managed within the model's context budget, so that follow-up requests remain usable.
19. As an inline user, I want a separate reset command in the bot's direct chat, so that I can start a fresh Inline conversation across destination chats.
20. As a direct-chat user, I want to see every tool action, so that the bot does not appear inactive while it works.
21. As a group user, I want to see tool actions associated with the request, so that participants can follow its progress.
22. As a Telegram user, I want shell commands included in tool activity, so that bash execution is visible.
23. As a Telegram user, I want file reads and changes included in tool activity, so that these actions are visible too.
24. As a Telegram user, I want search and other tools handled consistently, so that visibility does not depend on the tool's name.
25. As a Telegram user, I want one short message per tool invocation, so that I can follow individual actions.
26. As a Telegram user, I want that same activity message updated when the action finishes, so that the call and its outcome stay together.
27. As a Telegram user, I want a concise tool result, so that I can understand what the action returned.
28. As a Telegram user, I want longer tool output available in a collapsible block, so that details remain accessible without dominating the chat.
29. As a Telegram user, I want failed tools to show a useful error, so that I know what prevented completion.
30. As a Telegram user, I want tool progress and the final answer delivered independently, so that activity updates do not erase or replace the answer.
31. As a Telegram user, I want conversation reset to clear the active history and summary, so that the next request starts a new dialog.
32. As a Telegram user, I want earlier dialogs and temporary notes excluded after reset, so that old context is not automatically restored.
33. As a Telegram user, I want the reset boundary to survive a restart, so that a fresh dialog remains fresh.
34. As a Telegram user, I want Saved facts retained after reset, so that information I intentionally preserved is still available.
35. As a Telegram user, I want archived dialogs available through an explicit request, so that reset does not destroy useful records.
36. As a Telegram user, I want a failed reset to report failure, so that I am not told that context was cleared when persistence failed.
37. As a Telegram user, I want reasoning excluded from answer previews and final replies, so that internal reasoning is not exposed as an answer.
38. As a Telegram user, I want intermediate tool-call commentary separated from the final answer stream, so that text does not appear and disappear as though it were the answer.
39. As a Telegram user, I want a limited additional attempt when the model returns no answer and no tool call, so that a recoverable empty result does not leave me without a response.
40. As a direct-chat user, I want temporary network failures and timeouts retried, so that brief interruptions do not abort the request immediately.
41. As an inline user, I want the same retry policy as direct-chat users, so that inline requests are equally resilient.
42. As a Telegram user, I want temporary rate limits and server errors retried within a bounded wait, so that a temporary API failure can recover.
43. As a Telegram user, I want API-requested rate-limit delays respected, so that retries do not immediately hit the same limit.
44. As a Telegram user, I want permanent request, authentication, and quota failures explained, so that I can act on the real problem.
45. As a Telegram user, I want a short retry status, so that waiting during recovery is understandable.
46. As a Telegram user, I want completed tool actions preserved during a retry, so that commands, writes, and other actions are not repeated.
47. As a Telegram user, I want a final reply or clear failure even when a preview update fails, so that the request does not silently stall.
48. As a Telegram user, I want cancellation to stop retries, so that a stopped request does not keep working.
49. As a bot operator, I want both Parallel and Exa supported, so that I can use either search provider.
50. As a bot operator, I want the primary search provider configurable, so that I can choose the service used for normal requests.
51. As a Telegram user, I want a backup search provider on temporary primary-provider failure, so that web search remains available.
52. As a Telegram user, I want source titles, URLs, and useful excerpts in search results, so that answers can refer to verifiable sources.
53. As an inline user, I want the same supported search providers available through allowed tools, so that search-backed inline answers work.
54. As a bot operator, I want existing search configuration and providers preserved, so that adding these providers does not break established setups.

## Implementation Decisions

- Use the glossary concepts Inline conversation, Tool activity message, Conversation reset, and Saved fact. Respect the accepted decisions to isolate inline history and separate reset from memory deletion.
- Extend existing modules and dependencies. Reuse Telegram channel delivery, agent turns and tool execution events, session persistence and summarization, memory retrieval, command handling, provider error classification, and the search-provider abstraction.
- Upgrade Telego to the latest stable version compatible with the current official Telegram Bot API. The versions verified during the discussion were Telego 1.12.1 and Bot API 10.3; recheck at implementation time.
- Use native Rich Markdown and Telegram's native inline/display mathematical expressions. Normalize model-emitted formatting where necessary rather than introducing a separate TeX renderer.
- Use native rich drafts only where Telegram supports them: direct chats. Groups and inline replies use their supported send/edit mechanisms. Preserve length handling, reply references, topic routing, and readable formatting-error fallback.
- Scope persisted inline history by agent and Telegram UID, separately from direct and group session identities. The same user's selected inline calls in different destination chats continue that Inline conversation. Different users and agents remain isolated.
- Retain transport message identity separately from conversation identity: an inline message identifies the reply destination, not the persisted conversation.
- Persist selected calls and replies, not every keystroke-driven inline query. Use existing context budgeting and summarization for bounded model input.
- Do not automatically import direct/group histories, summaries, or their private memory into inline context. Apply the inline boundary to retrieval and other automatically supplied conversational context, not only the transcript.
- Add /clear_inline in the bot's direct chat. It resets the invoking user's Inline conversation for the current agent across destination chats. Normal /clear resets the current conversation.
- Preserve the existing inline tool restrictions. Adding inline memory does not enable bash, file writes, or other currently disallowed tools in inline mode.
- Show one Tool activity message for every executed tool invocation in direct chats and groups, including bash, file operations, searches, and other registered tools. Existing visibility configuration must not accidentally suppress the agreed group behavior.
- Show a useful command/action preview at start, then update that same message with a concise result or error. Keep longer output in a collapsible block within Telegram's limits; clearly indicate truncation when necessary.
- Give each activity message a stable identity associated with its invocation and request. Progress delivery must not consume the final-answer placeholder or produce duplicate messages for the same result.
- Reuse execution start/end events and add result information to the existing delivery contract where required. Keep tool implementations independent of Telegram presentation.
- Establish a persistent Conversation reset boundary across history, summaries, automatic archive retrieval, and temporary notes. Earlier context cannot re-enter automatically after restart. Saved facts and archived dialogs remain stored, and explicit archive requests retain existing access controls.
- Distinguish intentionally retained Saved facts from temporary notes when composing context. Do not treat the full workspace-memory payload as evidence that all its contents are intentionally retained facts.
- Preserve the reset boundary during in-flight work so that a late completion cannot silently repopulate the conversation that was reset. Report storage failures instead of confirming a successful reset.
- Remove reasoning-as-final-answer fallback. Carry provider-supplied content type/phase through streaming where available. Buffer ambiguous intermediate text until it is known to belong to the answer; tool-call commentary belongs outside the final-answer stream.
- Allow one additional attempt to obtain an answer when a model response contains neither an answer nor a tool call, within the overall request budget. If recovery fails, send a clear failure rather than reasoning.
- Use a shared policy for model requests in direct and inline conversations: at most two retries for transient network failures, timeouts, and temporary server failures, with increasing delays.
- Respect Retry-After for temporary HTTP 429 responses within a bounded overall wait. Treat authentication failures, invalid requests, and exhausted quota as clear failures. Respect user cancellation.
- Coordinate provider SDK retries, model fallback, and application retries under one bounded request budget. Do not multiply attempts through nested retry loops.
- Retry the failed operation, not the whole turn. Previously completed tool actions must not be replayed as a recovery mechanism. Tool-specific retry behavior must respect whether an action can safely be repeated.
- Deliver a short retry status separately from the answer. Telegram transport recovery is separate from model retries; a failed preview edit must still lead to a final outcome.
- Add Parallel and Exa adapters behind the existing SearchProvider contract and web_search tool. Reuse the current normalized search-result shape.
- Make primary-provider selection configurable, defaulting to Parallel with Exa as the backup for transient failures when credentials are available. Permit Exa as the configured primary with Parallel as its backup.
- Keep request schemas, authentication, extraction of excerpts, and provider-specific options inside their adapters. Use the existing credential/configuration mechanism; preserve existing providers and legacy configurations.
- Maintain this specification and the domain documentation alongside the implementation. The integration branch is `feat/ivo-80-telegram-experience`, based on dev and the existing specification branch.

## Testing Decisions

- The user confirmed the test strategy: prefer full scenarios from an incoming direct/group message or selected inline call through the agent turn to observable bot output. Substitute external model/Telegram APIs; use test HTTP servers for search contracts and a separate live Telegram check for formula rendering.
- Use existing high-level agent-loop and message-bus boundaries as the main behavioral seam. Reuse the established recording providers, fake channels, streaming sinks, and temporary persisted stores rather than creating a test hook in every module.
- Good tests assert externally observable behavior: delivered messages and edits, final outcomes, context passed to the model, persisted conversation behavior after reopening storage, HTTP request/response contracts, and counts of actual external requests or tool executions. Avoid assertions tied to helper structure, private fields, or incidental call order.
- Prefer one scenario-level test to several overlapping lower-level tests. Add focused adapter contract tests only where an external protocol cannot be represented adequately through the main seam.
- Rich delivery: exercise direct, group/topic, and inline paths; verify supported rich payloads, inline/display formula preservation, long content, reply association, and readable fallback. Live Telegram checks establish actual visual rendering; inspecting an API payload alone does not prove that TeX is displayed correctly.
- Inline persistence: selected calls continue across destination chats and a storage reopen, while different UIDs/agents and direct/group contexts stay isolated. Abandoned queries do not become conversation history. Long history remains within the context budget.
- Tool activity: cover a shell command, a file operation, a search, and an arbitrary registered tool, including success and failure. Assert one created message per invocation, an update with its outcome, appropriate long-output handling, and uninterrupted final-answer delivery. Include multiple calls in one turn.
- Conversation reset: seed history, summary, archived excerpts, temporary notes, and a Saved fact. Reset, reopen storage, and make a relevant follow-up request; old dialog context and notes must remain absent, while the Saved fact remains. An explicit archive request may retrieve authorized history. Include storage failure and a reset racing with an active turn.
- Reasoning: use a scripted provider that emits distinct reasoning, intermediate commentary, tool calls, and final content. Assert reasoning is never delivered as an answer, tool commentary does not flicker in the answer, and reasoning-only/empty responses receive bounded recovery followed by an answer or clear error.
- Retry behavior: cover transient network failures, timeouts, temporary 429 with requested delay, temporary server errors, permanent authentication/format/quota failures, cancellation, and exhausted attempts in both direct and inline modes. Verify a bounded request count without real backoff sleeps in tests.
- Replay prevention: execute a recording tool before a later model request fails, then recover that request. Assert the tool was executed once and its result is retained.
- Telegram recovery: fail an intermediate edit and confirm that final delivery or a clear failure still occurs. Ensure progress retries do not duplicate activity or final messages.
- Search contracts: use the existing HTTP-test-server approach for both provider adapters. Verify authentication/request shape, titles/URLs/excerpts, primary selection, reverse primary selection, transient fallback, unavailable credentials, and a clear outcome when both providers fail.
- Prior art exists in agent-loop command/context/exhaustion tests, recording-provider tests, inline-mode tests, streaming-sink tests, channel-manager delivery retry tests, Telegram inline/dispatch tests, persisted-session tests, cross-chat memory-retrieval tests, and web-tool HTTP contract tests. Extend these patterns instead of copying assertions about implementation internals.
- Run the focused package tests appropriate to each implementation increment and the repository's required checks. Live Telegram and paid search checks need configured credentials and are distinct from deterministic automated acceptance tests.

## Out of Scope

- Deployment, production configuration changes, force pushes, or merging the feature branch.
- Granting shell/file access in inline mode or broadening existing tool permissions.
- Sharing direct/group conversation memory with inline calls or sharing inline history across users.
- Automatically running both paid search providers for every search, or building a multi-engine ranking/merging system.
- Adding deep-research workflows, a separate search-generated answer pipeline, or a new search UI.
- Permanently deleting archived dialogs or Saved facts as part of /clear or /clear_inline.
- A new full memory-deletion command; reset and deletion remain distinct concepts, and this spec implements reset.
- Displaying reasoning in user-facing answer streams.
- Replaying a whole turn or blindly retrying side-effecting tools.
- Replacing the Telegram client, session backend, model-provider architecture, or existing tool registry.
- Building a custom formula renderer, image-based TeX pipeline, or Telegram Mini App.

## Further Notes

- Publish this as one implementation specification in the Ivoxygen team's Picoclaw project, with the ready-for-agent label.
- The original specification branch is `feat/telegram-experience`; implementation lives on `feat/ivo-80-telegram-experience` and targets dev. Domain decisions were recorded in commits ff5412a4 and 6b9f27c8.
- Current-state findings are evidence for the work, not proof of the deployed configuration: inline history is disabled; reset clears history/summary without excluding other context; reasoning is used when the final answer is empty; tool progress can interfere with the answer placeholder; ordinary direct/group processing suppresses some user-facing tool results.
- Archive retrieval is disabled by default. Investigate the deployed configuration before attributing the reported reset symptom specifically to archive retrieval. Workspace memory and recent notes are additional context sources.
- Existing retries differ by layer: timeout retries are shared by direct/inline agent processing, some provider SDKs add retries, model fallback handles classified failures, and some streaming-edit errors disable updates. Consolidation must preserve useful recovery while bounding total attempts.
- Deliver working increments in this order: reasoning/retries; Telegram upgrade and rich formulas; tool activity delivery; persistent reset boundaries; isolated inline history/reset; Parallel and Exa adapters.
- Official references: [Telegram rich messages](https://core.telegram.org/bots/api#rich-messages), [rich drafts](https://core.telegram.org/bots/api#sendrichmessagedraft), [Telego releases](https://github.com/mymmrac/telego/releases), [Parallel search](https://docs.parallel.ai/search/search-quickstart), [Exa search](https://exa.ai/docs/reference/search).

## Implementation notes

- [Telegram delivery](../channels/telegram/README.md) documents native Rich Markdown, formulas, direct drafts, group/inline edits, activity updates, and long-answer pagination. Published inline pages retain the complete result for up to 30 days, bounded by 256 results and 64 MiB; expired navigation reports its status explicitly.
- [Conversation memory](../conversation-memory.md) documents persistent reset generations, Saved-fact classification, isolated selected inline history, and `/clear_inline`. Existing unclassified memory remains stored; an explicit Saved facts heading or `SAVED_FACTS.md` distinguishes intentionally retained content from temporary notes.
- [Model recovery](../providers.md) documents the common three-attempt model-operation budget, bounded Retry-After waiting, empty-answer recovery, and preservation of completed tools.
- [Web search](../tools/web-search.md) documents configurable Parallel/Exa primary and transient fallback. Parallel uses the current GA `/v1/search` contract, checked with Context7 and official documentation during implementation.
- Deterministic acceptance tests substitute model/Telegram APIs and use temporary persistence and HTTP servers on ephemeral ports. Native formula appearance and paid-provider live checks remain separate checks requiring dedicated test credentials. Production containers, ports, configuration, and deployments are unchanged.
