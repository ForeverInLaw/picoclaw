import { type ReactNode, useState } from "react"
import { useTranslation } from "react-i18next"

import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"

interface EditableNameProps {
  label: string
  value: string
  disabled: boolean
  onSave: (name: string) => void
  children: ReactNode
}

// Shows a pool name or key label with a Rename action that edits it in place.
export function EditableName({
  label,
  value,
  disabled,
  onSave,
  children,
}: EditableNameProps) {
  const { t } = useTranslation()
  const [editing, setEditing] = useState(false)
  if (!editing)
    return (
      <div className="flex flex-wrap items-center gap-2">
        {children}
        <Button
          variant="ghost"
          size="sm"
          className="text-muted-foreground"
          disabled={disabled}
          onClick={() => setEditing(true)}
        >
          {t("credentials.keyPools.rename")}
        </Button>
      </div>
    )
  return (
    <form
      className="flex max-w-md gap-2"
      onSubmit={(event) => {
        event.preventDefault()
        const name = new FormData(event.currentTarget).get("name")
        if (typeof name === "string" && name.trim() && name.trim() !== value)
          onSave(name.trim())
        setEditing(false)
      }}
    >
      <Input
        name="name"
        aria-label={label}
        defaultValue={value}
        required
        maxLength={200}
        autoFocus
        disabled={disabled}
      />
      <Button type="submit" variant="outline" disabled={disabled}>
        {t("common.save")}
      </Button>
      <Button type="button" variant="ghost" onClick={() => setEditing(false)}>
        {t("common.cancel")}
      </Button>
    </form>
  )
}
