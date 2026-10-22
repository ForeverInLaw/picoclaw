import { useQuery } from "@tanstack/react-query"
import { useTranslation } from "react-i18next"

import { getCodexAccounts } from "@/api/codex-accounts"
import { Badge } from "@/components/ui/badge"

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
    <ul className="flex flex-col gap-3">
      {accounts.data.accounts.map((account) => (
        <li key={account.id} className="flex flex-col gap-1">
          <div className="flex flex-wrap items-center gap-2">
            <span>{account.label}</span>
            <Badge variant="secondary">
              {t(`credentials.accounts.${account.status}`)}
            </Badge>
          </div>
          {account.account_id && <p>{account.account_id}</p>}
          <p>
            {t("credentials.accounts.models")}:{" "}
            {account.models.join(", ") || "—"}
          </p>
        </li>
      ))}
    </ul>
  )
}
