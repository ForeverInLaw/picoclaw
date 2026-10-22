import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { useState } from "react"
import { useTranslation } from "react-i18next"

import { completeCodexOAuth, getOAuthFlow, loginOAuth } from "@/api/oauth"
import { Field } from "@/components/shared-form"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetFooter,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet"

export function CodexSignIn({
  accountID,
  label,
}: {
  accountID?: string
  label?: string
}) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [flowID, setFlowID] = useState("")
  const [open, setOpen] = useState(false)
  const [callback, setCallback] = useState("")
  const start = useMutation({
    mutationFn: () =>
      loginOAuth({
        provider: "openai",
        method: "browser",
        account_id: accountID,
      }),
  })
  const flow = useQuery({
    queryKey: ["codex-oauth-flow", flowID],
    queryFn: () => getOAuthFlow(flowID),
    enabled: !!flowID,
    refetchInterval: (query) =>
      query.state.data?.status === "pending" ? 2000 : false,
  })
  const complete = useMutation({
    mutationFn: (callbackURL: string) =>
      completeCodexOAuth(flowID, callbackURL),
    onSuccess: async (completed) => {
      queryClient.setQueryData(["codex-oauth-flow", flowID], completed)
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ["codex-accounts"] }),
        queryClient.invalidateQueries({ queryKey: ["oauth-providers"] }),
      ])
    },
    onSettled: () => setCallback(""),
  })
  const pending = flowID && (!flow.data || flow.data.status === "pending")

  const begin = () => {
    if (pending) {
      setOpen(true)
      return
    }
    const tab = window.open("about:blank", "_blank")
    if (tab) tab.opener = null
    complete.reset()
    start.mutate(undefined, {
      onSuccess: (response) => {
        if (!response.flow_id || !response.auth_url) {
          tab?.close()
          return
        }
        if (tab) tab.location.href = response.auth_url
        setFlowID(response.flow_id)
        setOpen(true)
      },
      onError: () => tab?.close(),
    })
  }
  const changeOpen = (next: boolean) => {
    setOpen(next)
    if (!next) {
      setCallback("")
      complete.reset()
      if (!pending) start.reset()
    }
  }

  return (
    <>
      <Button onClick={begin} disabled={start.isPending} variant="outline">
        {pending
          ? t("credentials.manual.resume")
          : label || t("credentials.manual.continueWithChatGPT")}
      </Button>
      {start.error && <p role="alert">{start.error.message}</p>}
      <Sheet open={open} onOpenChange={changeOpen}>
        <SheetContent>
          <SheetHeader>
            <SheetTitle>{t("credentials.manual.title")}</SheetTitle>
            <SheetDescription>
              {t("credentials.manual.instructions")}
            </SheetDescription>
          </SheetHeader>
          <div className="flex flex-col gap-4 px-4">
            <p className="text-muted-foreground text-sm">
              {t("credentials.manual.loopbackError")}
            </p>
            {start.data?.auth_url && pending && (
              <Button
                variant="outline"
                onClick={() =>
                  window.open(
                    start.data?.auth_url,
                    "_blank",
                    "noopener,noreferrer",
                  )
                }
              >
                {t("credentials.manual.openBrowser")}
              </Button>
            )}
            {pending && (
              <Field label={t("credentials.manual.callbackLabel")}>
                <Input
                  aria-label={t("credentials.manual.callbackLabel")}
                  value={callback}
                  onChange={(event) => setCallback(event.target.value)}
                  placeholder="http://127.0.0.1:1455/auth/callback?…"
                  autoComplete="off"
                  spellCheck={false}
                  disabled={complete.isPending}
                />
              </Field>
            )}
            {flow.data?.status === "success" && (
              <p role="status">{t("credentials.manual.success")}</p>
            )}
            {(complete.error || flow.error || flow.data?.error) && (
              <p role="alert">
                {complete.error?.message ||
                  flow.error?.message ||
                  flow.data?.error}
              </p>
            )}
          </div>
          <SheetFooter>
            <Button variant="ghost" onClick={() => changeOpen(false)}>
              {t("common.close")}
            </Button>
            {pending && (
              <Button
                disabled={!callback.trim() || complete.isPending}
                onClick={() => complete.mutate(callback.trim())}
              >
                {t("credentials.manual.complete")}
              </Button>
            )}
          </SheetFooter>
        </SheetContent>
      </Sheet>
    </>
  )
}
