import { IconLoader2 } from "@tabler/icons-react"
import { useMutation, useQuery } from "@tanstack/react-query"
import { useState } from "react"
import { useTranslation } from "react-i18next"

import { getCodexAccounts } from "@/api/codex-accounts"
import type { ModelTestConfiguration } from "@/api/model-probe"
import { addModel, setDefaultModel } from "@/api/models"
import { maskedSecretPlaceholder } from "@/components/secret-placeholder"
import {
  AdvancedSection,
  Field,
  KeyInput,
  SwitchCardField,
} from "@/components/shared-form"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetFooter,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet"

import { ModelProbe } from "./model-probe"

interface AddForm {
  modelName: string
  model: string
  apiBase: string
  apiKey: string
  proxy: string
  authMethod: string
  accountID: string
  connectMode: string
  workspace: string
  rpm: string
  maxTokensField: string
  requestTimeout: string
  thinkingLevel: string
}

const EMPTY_ADD_FORM: AddForm = {
  modelName: "",
  model: "",
  apiBase: "",
  apiKey: "",
  proxy: "",
  authMethod: "",
  accountID: "",
  connectMode: "",
  workspace: "",
  rpm: "",
  maxTokensField: "",
  requestTimeout: "",
  thinkingLevel: "",
}

interface AddModelSheetProps {
  open: boolean
  onClose: () => void
  onSaved: () => void
  existingModelNames: string[]
}

export function AddModelSheet(props: AddModelSheetProps) {
  return <AddModelForm key={props.open ? "open" : "closed"} {...props} />
}

function AddModelForm({
  open,
  onClose,
  onSaved,
  existingModelNames,
}: AddModelSheetProps) {
  const { t } = useTranslation()
  const [form, setForm] = useState<AddForm>(EMPTY_ADD_FORM)
  const accounts = useQuery({
    queryKey: ["codex-accounts"],
    queryFn: getCodexAccounts,
    enabled: open,
  })
  const save = useMutation({
    mutationFn: async () => {
      const result = await addModel(configuration)
      if (setAsDefault)
        await setDefaultModel(
          result.id ? `model-id:${result.id}` : configuration.model_name,
        )
    },
    onSuccess: () => {
      onSaved()
      onClose()
    },
  })
  const saving = save.isPending
  const [setAsDefault, setSetAsDefault] = useState(false)
  const [fieldErrors, setFieldErrors] = useState<
    Partial<Record<keyof AddForm, string>>
  >({})
  const [serverError, setServerError] = useState("")
  const apiKeyPlaceholder = maskedSecretPlaceholder(
    form.apiKey,
    t("models.field.apiKeyPlaceholder"),
  )

  const validate = (): boolean => {
    const errors: Partial<Record<keyof AddForm, string>> = {}
    const modelName = form.modelName.trim()
    if (!modelName) {
      errors.modelName = t("models.add.errorRequired")
    } else if (existingModelNames.some((name) => name.trim() === modelName)) {
      errors.modelName = t("models.add.errorDuplicateModelName")
    }
    if (!form.model.trim()) errors.model = t("models.add.errorRequired")
    setFieldErrors(errors)
    return Object.keys(errors).length === 0
  }

  const setField =
    (key: keyof AddForm) => (e: React.ChangeEvent<HTMLInputElement>) => {
      setForm((f) => ({ ...f, [key]: e.target.value }))
      if (fieldErrors[key]) {
        setFieldErrors((prev) => ({ ...prev, [key]: undefined }))
      }
    }

  const configuration: ModelTestConfiguration = {
    model_name: form.modelName.trim(),
    model: form.model.trim(),
    api_base: form.apiBase.trim() || undefined,
    api_key: form.apiKey.trim() || undefined,
    proxy: form.proxy.trim() || undefined,
    auth_method: form.authMethod.trim() || undefined,
    account_id: form.accountID || undefined,
    connect_mode: form.connectMode.trim() || undefined,
    workspace: form.workspace.trim() || undefined,
    rpm: form.rpm ? Number(form.rpm) : undefined,
    max_tokens_field: form.maxTokensField.trim() || undefined,
    request_timeout: form.requestTimeout
      ? Number(form.requestTimeout)
      : undefined,
    thinking_level: form.thinkingLevel.trim() || undefined,
  }

  const handleSave = () => {
    if (!validate()) return
    setServerError("")
    save.mutate(undefined, {
      onError: (error) => setServerError(error.message),
    })
  }

  return (
    <Sheet open={open} onOpenChange={(v) => !v && onClose()}>
      <SheetContent
        side="right"
        className="flex flex-col gap-0 p-0 data-[side=right]:!w-full data-[side=right]:sm:!w-[560px] data-[side=right]:sm:!max-w-[560px]"
      >
        <SheetHeader className="border-b-muted border-b px-6 py-5">
          <SheetTitle className="text-base">{t("models.add.title")}</SheetTitle>
          <SheetDescription className="text-xs">
            {t("models.add.description")}
          </SheetDescription>
        </SheetHeader>

        <div className="min-h-0 flex-1 overflow-y-auto">
          <div className="space-y-5 px-6 py-5">
            <Field
              label={t("models.add.modelName")}
              hint={t("models.add.modelNameHint")}
            >
              <Input
                value={form.modelName}
                onChange={setField("modelName")}
                placeholder={t("models.add.modelNamePlaceholder")}
                aria-invalid={!!fieldErrors.modelName}
              />
              {fieldErrors.modelName && (
                <p className="text-destructive text-xs">
                  {fieldErrors.modelName}
                </p>
              )}
            </Field>

            <Field
              label={t("models.add.modelId")}
              hint={t("models.add.modelIdHint")}
            >
              <Input
                value={form.model}
                onChange={setField("model")}
                placeholder={t("models.add.modelIdPlaceholder")}
                className="font-mono text-sm"
                aria-invalid={!!fieldErrors.model}
              />
              {fieldErrors.model && (
                <p className="text-destructive text-xs">{fieldErrors.model}</p>
              )}
            </Field>

            {(!form.model.includes("/") ||
              form.model.startsWith("openai/")) && (
              <Field label={t("credentials.accounts.connection")}>
                <Select
                  value={form.accountID || "api-key"}
                  onValueChange={(id) =>
                    setForm((current) => ({
                      ...current,
                      accountID: id === "api-key" ? "" : id,
                      authMethod: id === "api-key" ? "" : "oauth",
                      apiKey: "",
                      apiBase: "",
                    }))
                  }
                >
                  <SelectTrigger
                    aria-label={t("credentials.accounts.connection")}
                    className="w-full"
                  >
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectGroup>
                      <SelectItem value="api-key">
                        {t("models.field.apiKey")}
                      </SelectItem>
                      {accounts.data?.accounts.map((account) => (
                        <SelectItem key={account.id} value={account.id}>
                          {account.label} · {account.id.slice(-8)}
                        </SelectItem>
                      ))}
                    </SelectGroup>
                  </SelectContent>
                </Select>
              </Field>
            )}
            <Field label={t("models.field.apiKey")}>
              <KeyInput
                value={form.apiKey}
                onChange={(v) => setForm((f) => ({ ...f, apiKey: v }))}
                placeholder={apiKeyPlaceholder}
              />
            </Field>

            <Field label={t("models.field.apiBase")}>
              <Input
                value={form.apiBase}
                onChange={setField("apiBase")}
                placeholder="https://api.example.com/v1"
              />
            </Field>

            <SwitchCardField
              label={t("models.defaultOnSave.label")}
              hint={t("models.defaultOnSave.description")}
              checked={setAsDefault}
              onCheckedChange={setSetAsDefault}
            />

            <AdvancedSection>
              <Field
                label={t("models.field.proxy")}
                hint={t("models.field.proxyHint")}
              >
                <Input
                  value={form.proxy}
                  onChange={setField("proxy")}
                  placeholder="http://127.0.0.1:7890"
                />
              </Field>

              <Field
                label={t("models.field.authMethod")}
                hint={t("models.field.authMethodHint")}
              >
                <Input
                  value={form.authMethod}
                  onChange={setField("authMethod")}
                  placeholder="oauth"
                />
              </Field>

              <Field
                label={t("models.field.connectMode")}
                hint={t("models.field.connectModeHint")}
              >
                <Input
                  value={form.connectMode}
                  onChange={setField("connectMode")}
                  placeholder="stdio"
                />
              </Field>

              <Field
                label={t("models.field.workspace")}
                hint={t("models.field.workspaceHint")}
              >
                <Input
                  value={form.workspace}
                  onChange={setField("workspace")}
                  placeholder="/path/to/workspace"
                />
              </Field>

              <Field
                label={t("models.field.requestTimeout")}
                hint={t("models.field.requestTimeoutHint")}
              >
                <Input
                  value={form.requestTimeout}
                  onChange={setField("requestTimeout")}
                  placeholder="60"
                  type="number"
                  min={0}
                />
              </Field>

              <Field
                label={t("models.field.rpm")}
                hint={t("models.field.rpmHint")}
              >
                <Input
                  value={form.rpm}
                  onChange={setField("rpm")}
                  placeholder="60"
                  type="number"
                  min={0}
                />
              </Field>

              <Field
                label={t("models.field.thinkingLevel")}
                hint={t("models.field.thinkingLevelHint")}
              >
                <Input
                  value={form.thinkingLevel}
                  onChange={setField("thinkingLevel")}
                  placeholder="off"
                />
              </Field>

              <Field
                label={t("models.field.maxTokensField")}
                hint={t("models.field.maxTokensFieldHint")}
              >
                <Input
                  value={form.maxTokensField}
                  onChange={setField("maxTokensField")}
                  placeholder="max_completion_tokens"
                />
              </Field>
            </AdvancedSection>

            <ModelProbe
              key={JSON.stringify(configuration)}
              configuration={configuration}
            />

            {serverError && (
              <p className="text-destructive bg-destructive/10 rounded-md px-3 py-2 text-sm">
                {serverError}
              </p>
            )}
          </div>
        </div>

        <SheetFooter className="border-t-muted border-t px-6 py-4">
          <Button variant="ghost" onClick={onClose} disabled={saving}>
            {t("common.cancel")}
          </Button>
          <Button onClick={handleSave} disabled={saving}>
            {saving && <IconLoader2 className="size-4 animate-spin" />}
            {t("models.add.confirm")}
          </Button>
        </SheetFooter>
      </SheetContent>
    </Sheet>
  )
}
