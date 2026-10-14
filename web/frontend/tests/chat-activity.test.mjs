import assert from "node:assert/strict"
import { registerHooks } from "node:module"
import { test } from "node:test"

registerHooks({
  resolve(specifier, context, nextResolve) {
    if (specifier.startsWith("@/")) {
      const path = new URL(`../src/${specifier.slice(2)}.ts`, import.meta.url)
      return nextResolve(path.href, context)
    }
    return nextResolve(specifier, context)
  },
})

const { handlePicoMessage } = await import("../src/features/chat/protocol.ts")
const { getChatState, updateChatStore } = await import("../src/store/chat.ts")
const { mergeHistoryMessages } = await import("../src/features/chat/history.ts")

test("thought and tool snapshots remain separate from one final answer after replay", () => {
  updateChatStore({
    messages: [
      { id: "request-1", role: "user", content: "run tool", timestamp: 1 },
    ],
    isTyping: true,
  })
  const receive = (type, payload) =>
    handlePicoMessage(
      { type, session_id: "activity", timestamp: 100, payload },
      "activity",
    )
  const thought = {
    message_id: "thought-1",
    reply_to_message_id: "request-1",
    kind: "reasoning",
    state: "started",
    result: "Supplied thought",
  }
  const started = {
    message_id: "tool-1",
    reply_to_message_id: "request-1",
    kind: "tool",
    tool: "exec",
    state: "started",
    arguments: { command: "echo hello" },
  }
  const completed = { ...started, state: "completed", result: "hello" }
  receive("activity.update", thought)
  receive("activity.update", started)
  receive("activity.update", completed)
  receive("message.create", {
    message_id: "preview-1",
    reply_to_message_id: "request-1",
    content: "an",
    final: false,
  })
  assert.equal(getChatState().isTyping, true)
  const final = {
    message_id: "final-1",
    replace_message_id: "preview-1",
    reply_to_message_id: "request-1",
    content: "answer",
    final: true,
  }
  receive("message.create", final)
  receive("message.create", final)
  receive("activity.update", { ...thought, state: "completed" })
  receive("activity.update", started)
  receive("activity.update", completed)
  receive("message.update", {
    message_id: "preview-1",
    reply_to_message_id: "request-1",
    content: "stale preview",
    final: false,
  })
  receive("message.delete", { message_id: "preview-1" })
  const state = getChatState()
  assert.equal(state.messages.length, 2)
  assert.equal(state.messages[1].id, "final-1")
  assert.equal(state.messages[1].content, "answer")
  assert.equal(state.isTyping, false)
  assert.deepEqual(
    state.messages[1].activities.map((activity) => [
      activity.kind,
      activity.state,
      activity.result,
    ]),
    [
      ["reasoning", "completed", "Supplied thought"],
      ["tool", "completed", "hello"],
    ],
  )
})

test("reconnect replay attaches activity to the final already restored from history", () => {
  updateChatStore({
    messages: [
      {
        id: "history-user",
        role: "user",
        content: "run tool",
        timestamp: 1,
        source: "history",
      },
      {
        id: "history-answer",
        role: "assistant",
        content: "answer",
        timestamp: 2,
        source: "history",
      },
    ],
  })
  handlePicoMessage(
    {
      type: "activity.update",
      payload: {
        message_id: "thought-replay",
        reply_to_message_id: "request-replay",
        kind: "reasoning",
        state: "completed",
        result: "Supplied thought",
      },
    },
    "activity",
  )
  handlePicoMessage(
    {
      type: "message.create",
      payload: {
        message_id: "final-replay",
        reply_to_message_id: "request-replay",
        content: "answer",
        final: true,
      },
    },
    "activity",
  )
  const state = getChatState()
  assert.equal(state.messages.length, 2)
  assert.equal(state.messages[1].id, "final-replay")
  assert.equal(state.messages[1].activities[0].result, "Supplied thought")
})

test("transport errors remain visible for each turn and other sessions stay isolated", () => {
  updateChatStore({
    messages: [{ id: "request-a", role: "user", content: "first", timestamp: 1 }],
    isTyping: true,
  })
  const error = { type: "error", payload: { code: "failed", message: "Request failed" } }
  handlePicoMessage({ ...error, session_id: "other" }, "activity")
  assert.equal(getChatState().messages.length, 1)
  assert.equal(getChatState().isTyping, true)
  handlePicoMessage(error, "activity")
  handlePicoMessage(error, "activity")
  updateChatStore((prev) => ({
    messages: [...prev.messages, { id: "request-b", role: "user", content: "second", timestamp: 2 }],
    isTyping: true,
  }))
  handlePicoMessage(error, "activity")
  const state = getChatState()
  assert.equal(state.messages.filter((message) => message.error).length, 2)
  assert.equal(state.isTyping, false)
})

test("history arriving after replay preserves one final and keeps repeated answers on later turns", () => {
  const history = [
    { id: "history-user", role: "user", content: "first", timestamp: 20, source: "history" },
    { id: "history-answer", role: "assistant", content: "answer", timestamp: 20, source: "history" },
  ]
  const replayed = {
    id: "final-replay", role: "assistant", content: "answer", timestamp: 21,
    status: "final", replyToMessageId: "request-replay",
    activities: [{ id: "thought", kind: "reasoning", state: "completed", result: "Supplied thought" }],
  }
  const merged = mergeHistoryMessages(history, [replayed])
  assert.equal(merged.length, 2)
  assert.equal(merged[1].id, "final-replay")
  assert.equal(merged[1].activities[0].result, "Supplied thought")
  const later = mergeHistoryMessages(history, [
    { id: "new-user", role: "user", content: "second", timestamp: 22 },
    { ...replayed, id: "new-final", replyToMessageId: "new-user", timestamp: 23 },
  ])
  assert.equal(later.length, 4)
})
