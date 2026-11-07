import { type ReactNode, useState } from "react"
import { useTranslation } from "react-i18next"

import { Button } from "@/components/ui/button"

interface InlineEditProps {
  view: ReactNode
  actionLabel: string
  disabled: boolean
  formClassName: string
  onSubmit: (form: FormData) => Promise<unknown> | undefined
  children: ReactNode
}

// Shows view with an action that swaps it in place for a form of children
// with Save and Cancel. Save hands the form's data to onSubmit, which returns
// the save's promise, or undefined when there is nothing to save. The form
// closes once the save succeeds; a failed save keeps it open with its values,
// and the caller shows the error.
export function InlineEdit({
  view,
  actionLabel,
  disabled,
  formClassName,
  onSubmit,
  children,
}: InlineEditProps) {
  const { t } = useTranslation()
  const [editing, setEditing] = useState(false)
  if (!editing)
    return (
      <div className="flex flex-wrap items-center gap-2">
        {view}
        <Button
          variant="ghost"
          size="sm"
          className="text-muted-foreground"
          disabled={disabled}
          onClick={() => setEditing(true)}
        >
          {actionLabel}
        </Button>
      </div>
    )
  return (
    <form
      className={formClassName}
      onSubmit={(event) => {
        event.preventDefault()
        const saved = onSubmit(new FormData(event.currentTarget))
        // A failed save leaves the form open; its caller shows the error.
        void (saved ?? Promise.resolve()).then(
          () => setEditing(false),
          () => undefined,
        )
      }}
    >
      {children}
      <Button type="submit" variant="outline" disabled={disabled}>
        {t("common.save")}
      </Button>
      <Button type="button" variant="ghost" onClick={() => setEditing(false)}>
        {t("common.cancel")}
      </Button>
    </form>
  )
}
