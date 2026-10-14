import type { ChatAttachment } from "@/store/chat"

export interface ImageLimits {
  max_bytes: number
  max_images: number
  allowed_types: string[]
}

async function mediaRequest(
  path: string,
  options?: RequestInit,
): Promise<Response> {
  const response = await fetch(path, { credentials: "same-origin", ...options })
  if (!response.ok) {
    const detail = await response.text()
    throw new Error(
      detail.trim() || `Image request failed (${response.status}).`,
    )
  }
  return response
}

export async function getImageLimits(sessionId: string): Promise<ImageLimits> {
  const response = await mediaRequest(
    `/pico/uploads?session_id=${encodeURIComponent(sessionId)}`,
  )
  return response.json()
}

export async function discardChatImages(
  images: ChatAttachment[],
): Promise<void> {
  await Promise.all(
    images.map((image) => mediaRequest(image.url, { method: "DELETE" })),
  )
}

// Finish the upload batch before publishing one text+image WebSocket message.
// Failed/canceled batches release their completed drafts independently of the
// aborted upload signal; the media store TTL covers unavailable cleanup routes.
export async function uploadChatImages(
  files: File[],
  sessionId: string,
  signal: AbortSignal,
): Promise<ChatAttachment[]> {
  const uploaded: ChatAttachment[] = []
  try {
    for (const file of files) {
      const body = new FormData()
      body.append("file", file)
      const response = await mediaRequest(
        `/pico/uploads?session_id=${encodeURIComponent(sessionId)}`,
        { method: "POST", body, signal },
      )
      const image: {
        ref: string
        url: string
        filename: string
        content_type: string
        size: number
      } = await response.json()
      uploaded.push({
        ref: image.ref,
        url: image.url,
        filename: image.filename,
        contentType: image.content_type,
        size: image.size,
      })
    }
    signal.throwIfAborted()
    return uploaded
  } catch (error) {
    await discardChatImages(uploaded).catch(() => undefined)
    throw error
  }
}
