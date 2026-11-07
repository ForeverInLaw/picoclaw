import type { ReactNode } from "react"
import { useTranslation } from "react-i18next"

import { Input } from "@/components/ui/input"

import { InlineEdit } from "./inline-edit"

interface EditableNameProps {
  label: string
  value: string
  disabled: boolean
  onSave: (name: string) => Promise<unknown>
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
  return (
    <InlineEdit
      view={children}
      actionLabel={t("credentials.keyPools.rename")}
      disabled={disabled}
      formClassName="flex max-w-md gap-2"
      onSubmit={(form) => {
        const name = form.get("name")
        if (typeof name === "string" && name.trim() && name.trim() !== value)
          return onSave(name.trim())
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
    </InlineEdit>
  )
}
