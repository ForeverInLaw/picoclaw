import type { ChatAttachment } from "@/store/chat"

interface UserMessageProps {
  content: string
  attachments?: ChatAttachment[]
}

export function UserMessage({ content, attachments }: UserMessageProps) {
  return (
    <div className="flex w-full flex-col items-end gap-1.5">
      {attachments && (
        <div className="flex max-w-[70%] flex-wrap justify-end gap-2">
          {attachments.map((image) => (
            <img
              key={image.ref}
              src={image.url}
              alt={image.filename}
              className="max-h-48 max-w-64 rounded-lg border object-contain"
            />
          ))}
        </div>
      )}
      {content && (
        <div className="max-w-[70%] rounded-2xl rounded-tr-sm bg-violet-500 px-5 py-3 text-[15px] leading-relaxed whitespace-pre-wrap text-white shadow-sm">
          {content}
        </div>
      )}
    </div>
  )
}
