import { IconLoader2 } from "@tabler/icons-react"
import { useMutation, useQuery } from "@tanstack/react-query"
import { useState } from "react"
import { useTranslation } from "react-i18next"

import { getCodexAccounts } from "@/api/codex-accounts"
import type { ModelTestConfiguration } from "@/api/model-probe"
import { type ModelInfo, setDefaultModel, updateModel } from "@/api/models"
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

interface EditForm {
  apiKey: string
  apiBase: string
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

interface EditModelSheetProps {
  model: ModelInfo | null
  open: boolean
  onClose: () => void
  onSaved: () => void
}

export function EditModelSheet(props: EditModelSheetProps) {
  return <EditModelForm key={props.model?.index ?? "closed"} {...props} />
}

function EditModelForm({ model, open, onClose, onSaved }: EditModelSheetProps) {
  const { t } = useTranslation()
  const [form, setForm] = useState<EditForm>(() => ({
    apiKey: "",
    apiBase: model?.api_base ?? "",
    proxy: model?.proxy ?? "",
    authMethod: model?.auth_method ?? "",
    accountID: model?.account_id ?? "",
    connectMode: model?.connect_mode ?? "",
    workspace: model?.workspace ?? "",
    rpm: model?.rpm ? String(model.rpm) : "",
    maxTokensField: model?.max_tokens_field ?? "",
    requestTimeout: model?.request_timeout ? String(model.request_timeout) : "",
    thinkingLevel: model?.thinking_level ?? "",
  }))
  const accounts = useQuery({
    queryKey: ["codex-accounts"],
    queryFn: getCodexAccounts,
    enabled: open,
  })
  const save = useMutation({
    mutationFn: async () => {
      if (!model || !configuration) return
      await updateModel(model.index, configuration)
      if (setAsDefault && !model.is_default)
        await setDefaultModel(model.model_name)
    },
    onSuccess: () => {
      onSaved()
      onClose()
    },
  })
  const saving = save.isPending
  const [setAsDefault, setSetAsDefault] = useState(model?.is_default ?? false)
  const [error, setError] = useState("")

  const setField =
    (key: keyof EditForm) => (e: React.ChangeEvent<HTMLInputElement>) =>
      setForm((f) => ({ ...f, [key]: e.target.value }))

  const configuration: ModelTestConfiguration | null = model
    ? {
        model_name: model.model_name,
        model: model.model,
        api_base: form.apiBase,
        api_key: form.apiKey || undefined,
        proxy: form.proxy,
        auth_method: form.authMethod,
        account_id: form.accountID,
        connect_mode: form.connectMode,
        workspace: form.workspace,
        rpm: form.rpm ? Number(form.rpm) : 0,
        max_tokens_field: form.maxTokensField,
        request_timeout: form.requestTimeout ? Number(form.requestTimeout) : 0,
        thinking_level: form.thinkingLevel,
        extra_body: model.extra_body,
      }
    : null

  const handleSave = () => {
    setError("")
    save.mutate(undefined, { onError: (e) => setError(e.message) })
  }

  const isOAuth = form.authMethod === "oauth" || form.authMethod === "token"
  const isCodex =
    isOAuth &&
    (model?.model.startsWith("openai/") || !model?.model.includes("/"))
  const apiKeyPlaceholder = model?.configured
    ? maskedSecretPlaceholder(
        model.api_key,
        t("models.field.apiKeyPlaceholderSet"),
      )
    : t("models.field.apiKeyPlaceholder")

  return (
    <Sheet open={open} onOpenChange={(v) => !v && onClose()}>
      <SheetContent
        side="right"
        className="flex flex-col gap-0 p-0 data-[side=right]:!w-full data-[side=right]:sm:!w-[560px] data-[side=right]:sm:!max-w-[560px]"
      >
        <SheetHeader className="border-b-muted border-b px-6 py-5">
          <SheetTitle className="text-base">
            {t("models.edit.title", { name: model?.model_name })}
          </SheetTitle>
          <SheetDescription className="font-mono text-xs">
            {model?.model}
          </SheetDescription>
        </SheetHeader>

        <div className="min-h-0 flex-1 overflow-y-auto">
          <div className="space-y-5 px-6 py-5">
            {isCodex && (
              <Field label={t("credentials.accounts.connection")}>
                <Select
                  value={form.accountID}
                  onValueChange={(value) =>
                    setForm((current) => ({ ...current, accountID: value }))
                  }
                >
                  <SelectTrigger
                    aria-label={t("credentials.accounts.connection")}
                    className="w-full"
                  >
                    <SelectValue
                      placeholder={t("credentials.accounts.select")}
                    />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectGroup>
                      {accounts.data?.accounts.map((account) => (
                        <SelectItem key={account.id} value={account.id}>
                          {account.label}
                        </SelectItem>
                      ))}
                    </SelectGroup>
                  </SelectContent>
                </Select>
                {accounts.error && <p role="alert">{accounts.error.message}</p>}
              </Field>
            )}
            {!isOAuth && (
              <Field
                label={t("models.field.apiKey")}
                hint={
                  model?.configured ? t("models.edit.apiKeyHint") : undefined
                }
              >
                <KeyInput
                  value={form.apiKey}
                  onChange={(v) => setForm((f) => ({ ...f, apiKey: v }))}
                  placeholder={apiKeyPlaceholder}
                />
              </Field>
            )}

            <Field
              label={t("models.field.apiBase")}
              hint={isOAuth ? t("models.edit.oauthNote") : undefined}
            >
              <Input
                value={form.apiBase}
                onChange={setField("apiBase")}
                placeholder="https://api.example.com/v1"
                disabled={isOAuth}
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

            {configuration && model && (
              <ModelProbe
                key={JSON.stringify({
                  ...configuration,
                  model_index: model.index,
                })}
                configuration={{ ...configuration, model_index: model.index }}
              />
            )}

            {error && (
              <p className="text-destructive bg-destructive/10 rounded-md px-3 py-2 text-sm">
                {error}
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
            {t("common.save")}
          </Button>
        </SheetFooter>
      </SheetContent>
    </Sheet>
  )
}
