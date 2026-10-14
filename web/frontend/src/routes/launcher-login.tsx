import { createFileRoute } from "@tanstack/react-router"

import { LauncherAuthPage } from "@/components/launcher-auth-page"

export const Route = createFileRoute("/launcher-login")({
  component: () => <LauncherAuthPage mode="login" />,
})
