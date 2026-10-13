import { IconLock } from "@tabler/icons-react"
import { useMutation } from "@tanstack/react-query"
import { useTranslation } from "react-i18next"
import { toast } from "sonner"

import { postLauncherDashboardLogout } from "@/api/launcher-auth"
import { Button } from "@/components/ui/button"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"

export function LauncherAuthMenu() {
  const { t } = useTranslation()
  const logout = useMutation({
    mutationFn: postLauncherDashboardLogout,
    onSuccess: () => globalThis.location.assign("/launcher-login"),
    onError: () => toast.error(t("dashboardAuth.logoutError")),
  })

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button
          variant="ghost"
          size="icon"
          aria-label={t("dashboardAuth.account")}
          disabled={logout.isPending}
        >
          <IconLock />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end">
        <DropdownMenuGroup>
          <DropdownMenuItem asChild>
            <a href="/launcher-setup">{t("dashboardAuth.changeTitle")}</a>
          </DropdownMenuItem>
          <DropdownMenuItem
            onClick={() => logout.mutate()}
            disabled={logout.isPending}
          >
            {t("dashboardAuth.logout")}
          </DropdownMenuItem>
        </DropdownMenuGroup>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
