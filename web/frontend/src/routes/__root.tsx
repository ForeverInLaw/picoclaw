import { useQuery } from "@tanstack/react-query"
import { Outlet, createRootRoute, useRouterState } from "@tanstack/react-router"
import { TanStackRouterDevtools } from "@tanstack/react-router-devtools"
import { useEffect } from "react"
import { useTranslation } from "react-i18next"

import { getLauncherAuthStatus } from "@/api/launcher-auth"
import { AppLayout } from "@/components/app-layout"
import { LauncherAuthPage } from "@/components/launcher-auth-page"
import { Button } from "@/components/ui/button"
import {
  initializeChatStore,
  teardownChatStore,
} from "@/features/chat/controller"

const AuthenticatedLayout = () => {
  useEffect(() => {
    initializeChatStore()
    return teardownChatStore
  }, [])

  return (
    <AppLayout>
      <Outlet />
      <TanStackRouterDevtools />
    </AppLayout>
  )
}

const RootLayout = () => {
  const { t } = useTranslation()
  const path = useRouterState({ select: (state) => state.location.pathname })
  const auth = useQuery({
    queryKey: ["launcher-auth"],
    queryFn: getLauncherAuthStatus,
    retry: false,
    refetchInterval: 30_000,
    refetchOnWindowFocus: "always",
  })
  if (auth.isPending)
    return (
      <main
        className="flex min-h-dvh items-center justify-center"
        role="status"
      >
        {t("dashboardAuth.loading")}
      </main>
    )
  if (auth.isError)
    return (
      <main className="flex min-h-dvh flex-col items-center justify-center gap-4 p-4">
        <p role="alert">{auth.error.message}</p>
        <Button onClick={() => void auth.refetch()}>
          {t("dashboardAuth.retry")}
        </Button>
      </main>
    )
  if (!auth.data.initialized)
    return <LauncherAuthPage key="setup" mode="setup" />
  if (!auth.data.authenticated)
    return <LauncherAuthPage key="login" mode="login" />
  if (path === "/launcher-login" || path === "/launcher-setup")
    return <Outlet />
  return <AuthenticatedLayout />
}

export const Route = createRootRoute({ component: RootLayout })
