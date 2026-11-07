import { type ReactNode, useState } from "react"
import { useTranslation } from "react-i18next"

import { Button } from "@/components/ui/button"

interface InlineEditProps {
  view: ReactNode
  actionLabel: string
  disabled: boolean
  formClassName: string
  onSubmit: (form: FormData) => void
  children: ReactNode
}

// Shows view with an action that swaps it in place for a form of children
// with Save and Cancel. Save hands the form's data to onSubmit.
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
        onSubmit(new FormData(event.currentTarget))
        setEditing(false)
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
