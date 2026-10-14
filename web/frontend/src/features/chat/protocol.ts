import { normalizeUnixTimestamp } from "@/features/chat/state"
import {
  type ChatActivity,
  type ChatMessage,
  updateChatStore,
} from "@/store/chat"

export interface PicoMessage {
  type: string
  id?: string
  session_id?: string
  timestamp?: number | string
  payload?: Record<string, unknown>
}

function readText(value: unknown): string {
  return typeof value === "string" ? value : ""
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value)
}

function eventTime(message: PicoMessage): number {
  const timestamp = Number(message.timestamp)
  return Number.isFinite(timestamp)
    ? normalizeUnixTimestamp(timestamp)
    : Date.now()
}

function turnMessageIndex(
  messages: ChatMessage[],
  id: string,
  replyTo: string,
  replacedId = "",
): number {
  return messages.findIndex(
    (message) =>
      message.id === id ||
      (replacedId !== "" && message.id === replacedId) ||
      (replyTo !== "" &&
        message.role === "assistant" &&
        message.replyToMessageId === replyTo),
  )
}

function restoredFinalIndex(messages: ChatMessage[], content: string): number {
  for (let index = messages.length - 1; index >= 0; index -= 1) {
    const message = messages[index]
    if (message.role === "user") return -1
    if (message.source === "history" && message.role === "assistant") {
      return message.content === content ? index : -1
    }
  }
  return -1
}

function activityFromPayload(
  payload: Record<string, unknown>,
): ChatActivity | null {
  const id = readText(payload.message_id)
  const kind = payload.kind
  const state = payload.state
  if (
    !id ||
    (kind !== "reasoning" &&
      kind !== "tool" &&
      kind !== "retry" &&
      kind !== "commentary") ||
    (state !== "started" && state !== "completed" && state !== "failed")
  )
    return null
  const args = payload.arguments
  return {
    id,
    kind,
    state,
    tool: readText(payload.tool),
    arguments: isRecord(args) ? args : undefined,
    result: readText(payload.result),
    error: readText(payload.error),
  }
}

export function handlePicoMessage(
  message: PicoMessage,
  expectedSessionId: string,
) {
  if (message.session_id && message.session_id !== expectedSessionId) return
  const payload = message.payload ?? {}
  const timestamp = eventTime(message)

  switch (message.type) {
    case "activity.update": {
      const activity = activityFromPayload(payload)
      if (!activity) return
      const replyTo =
        readText(payload.reply_to_message_id) || readText(payload.request_id)
      updateChatStore((prev) => {
        const index = turnMessageIndex(
          prev.messages,
          `turn:${replyTo}`,
          replyTo,
        )
        const current = prev.messages[index]
        const previous = current?.activities?.find(
          (item) => item.id === activity.id,
        )
        if (
          previous &&
          previous.state !== "started" &&
          activity.state === "started"
        )
          return {}
        const activities = current?.activities ?? []
        const nextActivities = previous
          ? activities.map((item) =>
              item.id === activity.id ? activity : item,
            )
          : [...activities, activity]
        const next: ChatMessage = {
          ...current,
          id: current?.id ?? `turn:${replyTo}`,
          role: "assistant",
          content: current?.content ?? "",
          timestamp: current?.timestamp ?? timestamp,
          replyToMessageId: replyTo,
          activities: nextActivities,
          status: current?.status ?? "streaming",
        }
        return {
          messages:
            index < 0
              ? [...prev.messages, next]
              : prev.messages.map((item, i) => (i === index ? next : item)),
        }
      })
      return
    }
    case "message.create":
    case "message.update": {
      const id =
        readText(payload.message_id) ||
        message.id ||
        `pico-${message.timestamp ?? Date.now()}`
      const replyTo = readText(payload.reply_to_message_id)
      const replacedId = readText(payload.replace_message_id)
      const final = payload.final !== false && payload.placeholder !== true
      updateChatStore((prev) => {
        const content = readText(payload.content)
        const historyIndex = final
          ? restoredFinalIndex(prev.messages, content)
          : -1
        const liveIndex = turnMessageIndex(
          prev.messages,
          id,
          replyTo,
          replacedId,
        )
        const index = liveIndex >= 0 ? liveIndex : historyIndex
        const current = prev.messages[index]
        if (current?.status === "final" && !final) return {}
        const next: ChatMessage = {
          ...current,
          id,
          role: "assistant",
          content,
          timestamp: current?.timestamp ?? timestamp,
          replyToMessageId: replyTo || current?.replyToMessageId,
          status: final ? "final" : "streaming",
          source: undefined,
        }
        return {
          messages:
            index < 0
              ? [...prev.messages, next]
              : prev.messages.flatMap((item, i) => {
                  if (i === historyIndex && i !== index) return []
                  return [i === index ? next : item]
                }),
          isTyping: final ? false : prev.isTyping,
        }
      })
      return
    }
    case "message.delete": {
      const id = readText(payload.message_id)
      updateChatStore((prev) => ({
        messages: prev.messages.flatMap((item) => {
          if (item.id !== id || item.status === "final") return [item]
          return item.activities?.length
            ? [{ ...item, id: `turn:${item.replyToMessageId}`, content: "" }]
            : []
        }),
      }))
      return
    }
    case "typing.start":
      updateChatStore({ isTyping: true })
      return
    case "typing.stop":
      updateChatStore({ isTyping: false })
      return
    case "error": {
      const error = readText(payload.message)
      updateChatStore((prev) => {
        const replyTo =
          readText(payload.reply_to_message_id) ||
          [...prev.messages].reverse().find((item) => item.role === "user")?.id ||
          ""
        const id = message.id || `error:${readText(payload.code)}:${replyTo}`
        return {
          messages:
            error && !prev.messages.some((item) => item.id === id)
              ? [
                  ...prev.messages,
                  {
                    id,
                    role: "assistant",
                    content: "",
                    timestamp,
                    status: "final",
                    replyToMessageId: replyTo,
                    error,
                  },
                ]
              : prev.messages,
          isTyping: false,
        }
      })
      return
    }
    case "pong":
      return
  }
}
