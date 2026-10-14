import { getSessionHistory } from "@/api/sessions"
import { normalizeUnixTimestamp } from "@/features/chat/state"
import type { ChatMessage } from "@/store/chat"

export async function loadSessionMessages(
  sessionId: string,
): Promise<ChatMessage[]> {
  const detail = await getSessionHistory(sessionId)
  const fallbackTime = detail.updated

  return detail.messages.map<ChatMessage>((message, index) => ({
    id: `hist-${index}-${Date.now()}`,
    role: message.role,
    content: message.content,
    timestamp: fallbackTime,
    source: "history",
  }))
}

function normalizeMessageTimestamp(timestamp: number | string): string {
  if (typeof timestamp === "number") {
    return String(normalizeUnixTimestamp(timestamp))
  }

  const trimmed = timestamp.trim()
  if (/^-?\d+(\.\d+)?$/.test(trimmed)) {
    return String(normalizeUnixTimestamp(Number(trimmed)))
  }

  const parsed = Date.parse(trimmed)
  return Number.isNaN(parsed) ? trimmed : String(parsed)
}

function messageSignature(message: ChatMessage): string {
  return `${message.role}\u0000${message.content}\u0000${normalizeMessageTimestamp(
    message.timestamp,
  )}`
}

function comparableTimestamp(timestamp: number | string): number {
  const normalized = normalizeMessageTimestamp(timestamp)
  const numeric = Number(normalized)
  return Number.isFinite(numeric) ? numeric : 0
}

export function mergeHistoryMessages(
  historyMessages: ChatMessage[],
  currentMessages: ChatMessage[],
): ChatMessage[] {
  const currentIds = new Set(currentMessages.map((message) => message.id))
  const currentSignatures = new Set(
    currentMessages.map((message) => messageSignature(message)),
  )
  const historyFinal = historyMessages.at(-1)
  const replayedFinal = currentMessages.at(-1)
  // Replay can precede history hydration. Only match its trailing answer when
  // no new local user turn has been added, preserving repeated later answers.
  const replayedHistoryId =
    !currentMessages.some((message) => message.role === "user") &&
    historyFinal?.role === "assistant" &&
    replayedFinal?.status === "final" &&
    replayedFinal.content === historyFinal.content
      ? historyFinal.id
      : undefined

  const merged = [
    ...historyMessages.filter(
      (message) =>
        !currentIds.has(message.id) &&
        message.id !== replayedHistoryId &&
        !currentSignatures.has(messageSignature(message)),
    ),
    ...currentMessages,
  ]

  return merged.sort(
    (left, right) =>
      comparableTimestamp(left.timestamp) -
      comparableTimestamp(right.timestamp),
  )
}
