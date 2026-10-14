import { useMutation, useQuery } from "@tanstack/react-query"
import { useEffect, useRef } from "react"
import { useTranslation } from "react-i18next"

import {
  discardChatImages,
  getImageLimits,
  uploadChatImages,
} from "@/api/chat-images"
import { sendChatMessage } from "@/features/chat/controller"
import { getChatState, useChatStore } from "@/store/chat"
import {
  addDraftImages,
  clearDraftImages,
  removeDraftImage,
  useChatComposerStore,
} from "@/store/chat-composer"

export function useChatImages(isConnected: boolean) {
  const { t } = useTranslation()
  const sessionId = useChatStore((state) => state.activeSessionId)
  const images = useChatComposerStore((state) => state.images)
  const error = useChatComposerStore((state) => state.error)
  const isDragActive = useChatComposerStore((state) => state.isDragActive)
  const uploadRef = useRef<AbortController | null>(null)
  const limits = useQuery({
    queryKey: ["chat-image-limits", sessionId],
    queryFn: () => getImageLimits(sessionId),
    enabled: isConnected,
    retry: false,
  })
  const upload = useMutation({
    mutationKey: ["chat-image-send"],
    mutationFn: async ({
      content,
      files,
      session,
      controller,
    }: {
      content: string
      files: File[]
      session: string
      controller: AbortController
    }) => {
      const attachments = await uploadChatImages(
        files,
        session,
        controller.signal,
      )
      if (
        controller.signal.aborted ||
        getChatState().activeSessionId !== session ||
        !sendChatMessage(content, attachments)
      ) {
        await discardChatImages(attachments).catch(() => undefined)
        throw new Error(
          controller.signal.aborted
            ? t("chat.images.canceled")
            : t("chat.images.disconnected"),
        )
      }
    },
    onSuccess: () => clearDraftImages(),
    onError: (failure, variables) => {
      if (getChatState().activeSessionId === variables.session) {
        useChatComposerStore.setState({
          error: variables.controller.signal.aborted
            ? t("chat.images.canceled")
            : failure.message,
        })
      }
    },
  })

  useEffect(
    () => () => {
      uploadRef.current?.abort()
      clearDraftImages()
    },
    [sessionId],
  )

  const addImages = (files: File[]) => {
    if (!files.length || uploadRef.current) return
    try {
      if (!limits.data)
        throw new Error(
          limits.error?.message || t("chat.images.limitsUnavailable"),
        )
      addDraftImages(files, limits.data)
    } catch (failure) {
      useChatComposerStore.setState({
        error:
          failure instanceof Error
            ? failure.message
            : t("chat.images.uploadFailed"),
      })
    }
  }

  const send = async (content: string) => {
    if (uploadRef.current || (!content.trim() && images.length === 0))
      return false
    const controller = new AbortController()
    uploadRef.current = controller
    useChatComposerStore.setState({ error: "" })
    try {
      await upload.mutateAsync({
        content,
        files: images.map((image) => image.file),
        session: sessionId,
        controller,
      })
      return true
    } catch {
      return false
    } finally {
      uploadRef.current = null
    }
  }

  return {
    images,
    error,
    isDragActive,
    addImages,
    send,
    removeImage: removeDraftImage,
    setDragActive: (active: boolean) =>
      useChatComposerStore.setState({ isDragActive: active }),
    cancelUpload: () => uploadRef.current?.abort(),
    isUploading: upload.isPending,
    canAttach: isConnected && Boolean(limits.data) && !upload.isPending,
    allowedTypes:
      limits.data?.allowed_types.join(",") ||
      "image/jpeg,image/png,image/gif,image/webp",
    limitsError: limits.error?.message,
  }
}
