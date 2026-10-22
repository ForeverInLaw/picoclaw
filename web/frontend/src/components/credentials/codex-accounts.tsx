import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { useState } from "react"
import { useTranslation } from "react-i18next"

import {
  type CodexAccount,
  deleteCodexAccount,
  getCodexAccounts,
  testCodexAccount,
  updateCodexAccount,
} from "@/api/codex-accounts"
import { CodexSignIn } from "@/components/credentials/codex-sign-in"
import { Field } from "@/components/shared-form"
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { FieldGroup } from "@/components/ui/field"
import { Input } from "@/components/ui/input"

function CodexAccountRow({ account }: { account: CodexAccount }) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [model, setModel] = useState("")
  const [deleteOpen, setDeleteOpen] = useState(false)
  const invalidate = () =>
    queryClient.invalidateQueries({ queryKey: ["codex-accounts"] })
  const patch = useMutation({
    mutationFn: (values: { label?: string; enabled?: boolean }) =>
      updateCodexAccount(account.id, values),
    onSuccess: invalidate,
  })
  const probe = useMutation({
    mutationFn: () => testCodexAccount(account.id, model.trim()),
    onSettled: invalidate,
  })
  const remove = useMutation({
    mutationFn: () => deleteCodexAccount(account.id),
    onSuccess: invalidate,
  })
  const busy = patch.isPending || remove.isPending

  return (
    <li className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center gap-2">
        <span>{account.label}</span>
        <Badge variant="secondary">
          {t(`credentials.accounts.${account.status}`)}
        </Badge>
      </div>
      <p className="text-muted-foreground text-sm break-all">
        {account.email && <>{account.email} · </>}
        {t("credentials.accounts.connection")}: {account.id}
      </p>
      <p className="text-sm">
        {t("credentials.accounts.models")}: {account.models.join(", ") || "—"}
      </p>
      <p className="text-sm">
        {t("credentials.accounts.lastSuccess")}:{" "}
        {account.last_success_at
          ? new Date(account.last_success_at).toLocaleString()
          : t("credentials.accounts.neverChecked")}
      </p>
      {account.last_probe && (
        <p className="text-sm" role="status">
          {t("credentials.accounts.lastProbe")}: {account.last_probe.model} ·{" "}
          {t(`credentials.accounts.probe.${account.last_probe.status}`)} ·{" "}
          {new Date(account.last_probe.at).toLocaleString()}
        </p>
      )}
      <form
        onSubmit={(event) => {
          event.preventDefault()
          const label = new FormData(event.currentTarget).get("label")
          if (typeof label === "string") patch.mutate({ label: label.trim() })
        }}
      >
        <FieldGroup>
          <Field label={t("credentials.accounts.name")}>
            <div className="flex gap-2">
              <Input
                key={account.label}
                name="label"
                aria-label={t("credentials.accounts.name")}
                defaultValue={account.label}
                required
                maxLength={200}
                disabled={busy}
              />
              <Button type="submit" variant="outline" disabled={busy}>
                {t("common.save")}
              </Button>
            </div>
          </Field>
        </FieldGroup>
      </form>
      <div className="flex flex-wrap gap-2">
        <CodexSignIn
          accountID={account.id}
          label={t("credentials.accounts.reconnect")}
        />
        <Button
          variant="outline"
          disabled={busy}
          onClick={() => patch.mutate({ enabled: !account.enabled })}
        >
          {t(
            account.enabled
              ? "credentials.accounts.disable"
              : "credentials.accounts.enable",
          )}
        </Button>
        <Button
          variant="destructive"
          disabled={busy}
          onClick={() => {
            remove.reset()
            setDeleteOpen(true)
          }}
        >
          {t("credentials.accounts.delete")}
        </Button>
      </div>
      <form
        onSubmit={(event) => {
          event.preventDefault()
          probe.mutate()
        }}
      >
        <FieldGroup>
          <Field
            label={t("credentials.accounts.testModel")}
            hint={t("credentials.accounts.testHint")}
          >
            <div className="flex flex-wrap gap-2">
              <Input
                aria-label={t("credentials.accounts.testModel")}
                value={model}
                onChange={(event) => setModel(event.target.value)}
                placeholder="gpt-6.1-sol"
                required
                maxLength={200}
                disabled={probe.isPending}
              />
              <Button
                type="submit"
                variant="outline"
                disabled={!account.enabled || !model.trim() || probe.isPending}
              >
                {t(
                  probe.isPending
                    ? "credentials.accounts.testing"
                    : "credentials.accounts.test",
                )}
              </Button>
            </div>
          </Field>
        </FieldGroup>
      </form>
      {(patch.error || probe.error || remove.error) && (
        <p role="alert">
          {patch.error?.message ||
            probe.error?.message ||
            remove.error?.message}
        </p>
      )}
      <AlertDialog open={deleteOpen} onOpenChange={setDeleteOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t("credentials.accounts.deleteTitle", { name: account.label })}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t("credentials.accounts.deleteHint")}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t("common.cancel")}</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              disabled={remove.isPending}
              onClick={() => remove.mutate()}
            >
              {t("credentials.accounts.delete")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </li>
  )
}

export function CodexAccounts() {
  const { t } = useTranslation()
  const accounts = useQuery({
    queryKey: ["codex-accounts"],
    queryFn: getCodexAccounts,
  })
  if (accounts.isPending) return <p>{t("credentials.accounts.loading")}</p>
  if (accounts.error) return <p role="alert">{accounts.error.message}</p>
  if (!accounts.data.accounts.length)
    return <p>{t("credentials.accounts.empty")}</p>
  return (
    <ul className="flex flex-col gap-6">
      {accounts.data.accounts.map((account) => (
        <CodexAccountRow key={account.id} account={account} />
      ))}
    </ul>
  )
}
