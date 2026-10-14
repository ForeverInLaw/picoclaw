import { create } from "zustand"

import type { ImageLimits } from "@/api/chat-images"

export interface DraftImage {
  id: string
  file: File
  previewUrl: string
}

interface ComposerState {
  images: DraftImage[]
  error: string
  isDragActive: boolean
}

export const useChatComposerStore = create<ComposerState>()(() => ({
  images: [],
  error: "",
  isDragActive: false,
}))

export function addDraftImages(files: File[], limits: ImageLimits) {
  const current = useChatComposerStore.getState().images
  const total = [...current.map((image) => image.file), ...files]
  if (total.length > limits.max_images)
    throw new Error(`Attach at most ${limits.max_images} images.`)
  if (total.some((file) => !limits.allowed_types.includes(file.type)))
    throw new Error(
      "Unsupported image type. Select an allowed JPEG, PNG, GIF or WebP image.",
    )
  if (total.reduce((bytes, file) => bytes + file.size, 0) > limits.max_bytes)
    throw new Error(
      `Attached images exceed the ${(limits.max_bytes / 1024 / 1024).toFixed(1)} MB size limit.`,
    )
  useChatComposerStore.setState({
    images: [
      ...current,
      ...files.map((file) => ({
        id: crypto.randomUUID(),
        file,
        previewUrl: URL.createObjectURL(file),
      })),
    ],
    error: "",
  })
}

export function removeDraftImage(id: string) {
  useChatComposerStore.setState((state) => ({
    images: state.images.filter((image) => {
      if (image.id !== id) return true
      URL.revokeObjectURL(image.previewUrl)
      return false
    }),
    error: "",
  }))
}

export function clearDraftImages() {
  for (const image of useChatComposerStore.getState().images)
    URL.revokeObjectURL(image.previewUrl)
  useChatComposerStore.setState({ images: [], error: "", isDragActive: false })
}
