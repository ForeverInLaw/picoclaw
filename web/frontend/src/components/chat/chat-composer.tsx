import { IconArrowUp, IconPhotoPlus, IconX } from "@tabler/icons-react"
import { type KeyboardEvent, useRef } from "react"
import { useTranslation } from "react-i18next"
import TextareaAutosize from "react-textarea-autosize"

import { Button } from "@/components/ui/button"
import type { useChatImages } from "@/hooks/use-chat-images"
import { cn } from "@/lib/utils"

interface ChatComposerProps {
  input: string
  onInputChange: (value: string) => void
  onSend: () => void
  isConnected: boolean
  hasDefaultModel: boolean
  media: ReturnType<typeof useChatImages>
}

export function ChatComposer({
  input,
  onInputChange,
  onSend,
  isConnected,
  hasDefaultModel,
  media,
}: ChatComposerProps) {
  const { t } = useTranslation()
  const canInput = isConnected && hasDefaultModel && !media.isUploading
  const fileInputRef = useRef<HTMLInputElement>(null)
  const canSend =
    canInput && (input.trim().length > 0 || media.images.length > 0)

  const handleKeyDown = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.nativeEvent.isComposing) return
    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault()
      if (canSend) onSend()
    }
  }

  return (
    <div className="bg-background shrink-0 px-4 pt-4 pb-[calc(1rem+env(safe-area-inset-bottom))] md:px-8 md:pb-8 lg:px-24 xl:px-48">
      <div
        className={cn(
          "bg-card border-border/80 mx-auto flex max-w-[1000px] flex-col rounded-2xl border p-3 shadow-md",
          media.isDragActive && "border-primary",
        )}
        onDragOver={(event) => {
          event.preventDefault()
          if (media.canAttach && hasDefaultModel) media.setDragActive(true)
        }}
        onDragLeave={(event) => {
          if (
            !(event.relatedTarget instanceof Node) ||
            !event.currentTarget.contains(event.relatedTarget)
          )
            media.setDragActive(false)
        }}
        onDrop={(event) => {
          event.preventDefault()
          media.setDragActive(false)
          if (canInput) media.addImages(Array.from(event.dataTransfer.files))
        }}
      >
        <input
          ref={fileInputRef}
          type="file"
          accept={media.allowedTypes}
          multiple
          hidden
          aria-label={t("chat.images.select")}
          disabled={!media.canAttach || !hasDefaultModel}
          onChange={(event) => {
            media.addImages(Array.from(event.target.files || []))
            event.target.value = ""
          }}
        />
        {media.images.length > 0 && (
          <div className="mb-3 flex flex-wrap gap-2 px-2">
            {media.images.map((image) => (
              <div
                key={image.id}
                className="relative size-20 overflow-hidden rounded-lg border"
              >
                <img
                  src={image.previewUrl}
                  alt={image.file.name}
                  className="size-full object-cover"
                />
                <Button
                  type="button"
                  variant="secondary"
                  size="icon"
                  className="absolute top-1 right-1 size-6"
                  aria-label={t("chat.images.remove")}
                  disabled={media.isUploading}
                  onClick={() => media.removeImage(image.id)}
                >
                  <IconX />
                </Button>
              </div>
            ))}
          </div>
        )}
        {(media.error || media.limitsError) && (
          <p className="text-destructive px-2 pb-2 text-sm" role="alert">
            {media.error || media.limitsError}
          </p>
        )}
        {media.isDragActive && (
          <p className="text-primary px-2 pb-2 text-sm">
            {t("chat.images.drop")}
          </p>
        )}
        <TextareaAutosize
          value={input}
          onChange={(e) => onInputChange(e.target.value)}
          onKeyDown={handleKeyDown}
          onPaste={(event) => {
            const files = Array.from(event.clipboardData.files)
            if (files.length && canInput) {
              event.preventDefault()
              media.addImages(files)
            }
          }}
          placeholder={t("chat.placeholder")}
          disabled={!canInput}
          className={cn(
            "placeholder:text-muted-foreground max-h-[200px] min-h-[60px] resize-none border-0 bg-transparent px-2 py-1 text-[15px] shadow-none transition-colors focus-visible:ring-0 focus-visible:outline-none dark:bg-transparent",
            !canInput && "cursor-not-allowed",
          )}
          minRows={1}
          maxRows={8}
        />

        <div className="mt-2 flex items-center justify-between px-1">
          <div className="flex items-center gap-1">
            <Button
              type="button"
              variant="ghost"
              size="icon"
              aria-label={t("chat.images.select")}
              disabled={!media.canAttach || !hasDefaultModel}
              onClick={() => fileInputRef.current?.click()}
            >
              <IconPhotoPlus />
            </Button>
            {media.isUploading && (
              <Button
                type="button"
                variant="ghost"
                size="sm"
                onClick={media.cancelUpload}
              >
                {t("chat.images.cancel")}
              </Button>
            )}
            {media.isUploading && (
              <span className="text-muted-foreground text-sm" role="status">
                {t("chat.images.uploading")}
              </span>
            )}
          </div>

          <Button
            size="icon"
            className="size-8 rounded-full bg-violet-500 text-white transition-transform hover:bg-violet-600 active:scale-95"
            onClick={onSend}
            disabled={!canSend}
            aria-label={t("chat.images.send")}
          >
            <IconArrowUp className="size-4" />
          </Button>
        </div>
      </div>
    </div>
  )
}
