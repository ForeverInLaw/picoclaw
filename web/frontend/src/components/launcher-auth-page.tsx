import { useMutation } from "@tanstack/react-query"
import { useState } from "react"
import { useTranslation } from "react-i18next"

import {
  postLauncherDashboardLogin,
  postLauncherDashboardSetup,
} from "@/api/launcher-auth"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardContent,
  CardDescription,
  CardFooter,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import {
  Field,
  FieldDescription,
  FieldGroup,
  FieldLabel,
} from "@/components/ui/field"
import { Input } from "@/components/ui/input"

export function LauncherAuthPage({
  mode,
}: {
  mode: "login" | "setup" | "change"
}) {
  const { t } = useTranslation()
  const [password, setPassword] = useState("")
  const [confirm, setConfirm] = useState("")
  const [setupToken, setSetupToken] = useState(() =>
    mode === "setup"
      ? (new URLSearchParams(globalThis.location.hash.slice(1)).get(
          "setup_token",
        ) ?? "")
      : "",
  )
  const settingPassword = mode !== "login"
  const mutation = useMutation({
    mutationFn: async () => {
      if (settingPassword) {
        if (password !== confirm) throw new Error(t("dashboardAuth.mismatch"))
        await postLauncherDashboardSetup(
          password,
          confirm,
          mode === "setup" ? setupToken.trim() : undefined,
        )
      } else {
        await postLauncherDashboardLogin(password)
      }
    },
    onSuccess: () =>
      globalThis.location.assign(settingPassword ? "/launcher-login" : "/"),
  })

  return (
    <main className="bg-background flex min-h-dvh items-center justify-center p-4">
      <Card className="w-full max-w-md">
        <CardHeader>
          <CardTitle>
            <h1>{t(`dashboardAuth.${mode}Title`)}</h1>
          </CardTitle>
          <CardDescription>
            {t(`dashboardAuth.${mode}Description`)}
          </CardDescription>
        </CardHeader>
        <CardContent>
          <form
            id="dashboard-auth"
            onSubmit={(event) => {
              event.preventDefault()
              mutation.mutate()
            }}
          >
            <FieldGroup>
              {mode === "setup" && (
                <Field
                  data-invalid={Boolean(mutation.error)}
                  data-disabled={mutation.isPending}
                >
                  <FieldLabel htmlFor="dashboard-setup-token">
                    {t("dashboardAuth.setupToken")}
                  </FieldLabel>
                  <Input
                    id="dashboard-setup-token"
                    name="setupToken"
                    type="password"
                    autoComplete="off"
                    required
                    value={setupToken}
                    onChange={(event) => setSetupToken(event.target.value)}
                    disabled={mutation.isPending}
                    aria-invalid={Boolean(mutation.error)}
                    aria-describedby="dashboard-setup-token-description"
                  />
                  <FieldDescription id="dashboard-setup-token-description">
                    {t("dashboardAuth.setupTokenDescription")}
                  </FieldDescription>
                </Field>
              )}
              <Field data-invalid={Boolean(mutation.error)}>
                <FieldLabel htmlFor="dashboard-password">
                  {t("dashboardAuth.password")}
                </FieldLabel>
                <Input
                  id="dashboard-password"
                  name="password"
                  type="password"
                  autoComplete={
                    settingPassword ? "new-password" : "current-password"
                  }
                  required
                  minLength={settingPassword ? 8 : undefined}
                  value={password}
                  onChange={(event) => setPassword(event.target.value)}
                  disabled={mutation.isPending}
                  aria-invalid={Boolean(mutation.error)}
                  aria-describedby={
                    mutation.error ? "dashboard-auth-error" : undefined
                  }
                />
              </Field>
              {settingPassword && (
                <Field data-invalid={Boolean(mutation.error)}>
                  <FieldLabel htmlFor="dashboard-confirm">
                    {t("dashboardAuth.confirm")}
                  </FieldLabel>
                  <Input
                    id="dashboard-confirm"
                    name="confirm"
                    type="password"
                    autoComplete="new-password"
                    required
                    minLength={8}
                    value={confirm}
                    onChange={(event) => setConfirm(event.target.value)}
                    disabled={mutation.isPending}
                    aria-invalid={Boolean(mutation.error)}
                    aria-describedby={
                      mutation.error ? "dashboard-auth-error" : undefined
                    }
                  />
                </Field>
              )}
              {mutation.error && (
                <p
                  id="dashboard-auth-error"
                  className="text-destructive text-sm"
                  role="alert"
                >
                  {mutation.error.message}
                </p>
              )}
            </FieldGroup>
          </form>
        </CardContent>
        <CardFooter className="flex gap-2">
          <Button
            form="dashboard-auth"
            type="submit"
            disabled={mutation.isPending}
          >
            {t(
              mutation.isPending
                ? "dashboardAuth.loading"
                : `dashboardAuth.${mode}Submit`,
            )}
          </Button>
          {mode === "change" && (
            <Button variant="outline" asChild>
              <a href="/">{t("common.cancel")}</a>
            </Button>
          )}
        </CardFooter>
      </Card>
    </main>
  )
}
