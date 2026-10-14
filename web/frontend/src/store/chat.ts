import { create } from "zustand"

import {
  getInitialActiveSessionId,
  writeStoredSessionId,
} from "@/features/chat/state"

export interface ChatActivity {
  id: string
  kind: "reasoning" | "tool" | "retry" | "commentary"
  state: "started" | "completed" | "failed"
  tool?: string
  arguments?: Record<string, unknown>
  result: string
  error?: string
}

export interface ChatMessage {
  id: string
  role: "user" | "assistant"
  content: string
  timestamp: number | string
  replyToMessageId?: string
  activities?: ChatActivity[]
  status?: "streaming" | "final"
  source?: "history"
  error?: string
  attachments?: ChatAttachment[]
}

export interface ChatAttachment {
  ref: string
  url: string
  filename: string
  contentType: string
  size: number
}

export type ConnectionState =
  | "disconnected"
  | "connecting"
  | "connected"
  | "error"

export interface ChatStoreState {
  messages: ChatMessage[]
  connectionState: ConnectionState
  isTyping: boolean
  activeSessionId: string
  hasHydratedActiveSession: boolean
}

type ChatStorePatch = Partial<ChatStoreState>

const DEFAULT_CHAT_STATE: ChatStoreState = {
  messages: [],
  connectionState: "disconnected",
  isTyping: false,
  activeSessionId: getInitialActiveSessionId(),
  hasHydratedActiveSession: false,
}

export const useChatStore = create<ChatStoreState>()(() => DEFAULT_CHAT_STATE)

export function getChatState() {
  return useChatStore.getState()
}

export function updateChatStore(
  patch:
    | ChatStorePatch
    | ((prev: ChatStoreState) => ChatStorePatch | ChatStoreState),
) {
  useChatStore.setState((prev) => {
    const nextPatch = typeof patch === "function" ? patch(prev) : patch
    const next = { ...prev, ...nextPatch }

    if (next.activeSessionId !== prev.activeSessionId) {
      writeStoredSessionId(next.activeSessionId)
    }

    return next
  })
}
