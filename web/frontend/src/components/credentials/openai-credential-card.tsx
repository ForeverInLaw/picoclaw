import { IconBrandOpenai, IconLoader2 } from "@tabler/icons-react"
import { useTranslation } from "react-i18next"

import type { OAuthProviderStatus } from "@/api/oauth"
import { Button } from "@/components/ui/button"

import { CodexAccounts } from "./codex-accounts"
import { CodexSignIn } from "./codex-sign-in"
import { CredentialCard } from "./credential-card"

interface OpenAICredentialCardProps {
  status?: OAuthProviderStatus
  activeAction: string
  onAskLogout: () => void
}

export function OpenAICredentialCard({
  status,
  activeAction,
  onAskLogout,
}: OpenAICredentialCardProps) {
  const { t } = useTranslation()
  const actionBusy = activeAction !== ""
  return (
    <CredentialCard
      title={
        <span className="inline-flex items-center gap-2">
          <span className="border-muted inline-flex size-6 items-center justify-center rounded-full border">
            <IconBrandOpenai className="size-3.5" />
          </span>
          <span>OpenAI</span>
        </span>
      }
      description={t("credentials.providers.openai.description")}
      authMethod={status?.auth_method}
      details={<CodexAccounts />}
      actions={<CodexSignIn />}
      footer={
        status?.logged_in ? (
          <Button
            variant="ghost"
            size="sm"
            disabled={actionBusy}
            onClick={onAskLogout}
            className="text-destructive hover:bg-destructive/10 hover:text-destructive"
          >
            {activeAction === "openai:logout" && (
              <IconLoader2 className="size-4 animate-spin" />
            )}
            {t("credentials.actions.logout")}
          </Button>
        ) : null
      }
    />
  )
}
