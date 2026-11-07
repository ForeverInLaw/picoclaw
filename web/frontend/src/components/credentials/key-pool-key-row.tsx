import { useState } from "react"
import { useTranslation } from "react-i18next"

import {
  type KeyPoolKey,
  type KeyPoolKeyPatch,
  keyStatesOutOfService,
  removePoolKey,
  returnPoolKeyToService,
  updatePoolKey,
} from "@/api/key-pools"
import { ConfirmDialog } from "@/components/confirm-dialog"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { useKeyPoolMutation } from "@/hooks/use-key-pools"

import { EditableName } from "./editable-name"
import { KeyBudget } from "./key-budget"

const stateBadge = {
  available: "secondary",
  unknown: "outline",
  disabled: "outline",
  rate_limited: "outline",
  exhausted: "destructive",
  rejected: "destructive",
} as const

const formatTime = (value: string) => new Date(value).toLocaleString()

// One key of a Key pool: its availability in the gateway, its spend against
// its budget and its actions.
export function KeyPoolKeyRow({
  poolID,
  poolKey,
}: {
  poolID: string
  poolKey: KeyPoolKey
}) {
  const { t } = useTranslation()
  const [removeOpen, setRemoveOpen] = useState(false)
  const update = useKeyPoolMutation((patch: KeyPoolKeyPatch) =>
    updatePoolKey(poolID, poolKey.id, patch),
  )
  const giveBack = useKeyPoolMutation(() =>
    returnPoolKeyToService(poolID, poolKey.id),
  )
  const remove = useKeyPoolMutation(() => removePoolKey(poolID, poolKey.id))
  const busy = update.isPending || giveBack.isPending || remove.isPending
  const actionError = update.error ?? giveBack.error

  return (
    <li className="flex flex-col gap-2 border-t pt-3 first:border-t-0 first:pt-0">
      <EditableName
        label={t("credentials.keyPools.keyLabel")}
        value={poolKey.label}
        disabled={busy}
        onSave={(label) => update.mutate({ label })}
      >
        <span className="font-medium">{poolKey.label}</span>
        <code className="text-muted-foreground text-xs">{poolKey.api_key}</code>
        <Badge variant={stateBadge[poolKey.state]}>
          {t(`credentials.keyPools.state.${poolKey.state}`)}
        </Badge>
      </EditableName>
      <KeyBudget
        poolKey={poolKey}
        disabled={busy}
        onSave={(patch) => update.mutate(patch)}
      />
      {poolKey.until && (
        <p className="text-muted-foreground text-xs">
          {t("credentials.keyPools.nextCheck", {
            time: formatTime(poolKey.until),
          })}
        </p>
      )}
      {poolKey.rate_limited_models?.map((limit) => (
        <p key={limit.model} className="text-muted-foreground text-xs">
          {t("credentials.keyPools.rateLimitedUntil", {
            model: limit.model,
            time: formatTime(limit.until),
          })}
        </p>
      ))}
      <div className="flex flex-wrap gap-2">
        {keyStatesOutOfService.has(poolKey.state) && (
          <Button
            variant="outline"
            size="sm"
            disabled={busy}
            onClick={() => giveBack.mutate()}
          >
            {t("credentials.keyPools.returnToService")}
          </Button>
        )}
        <Button
          variant="outline"
          size="sm"
          disabled={busy}
          onClick={() => update.mutate({ enabled: !poolKey.enabled })}
        >
          {t(
            poolKey.enabled
              ? "credentials.keyPools.disable"
              : "credentials.keyPools.enable",
          )}
        </Button>
        <Button
          variant="ghost"
          size="sm"
          className="text-destructive hover:bg-destructive/10 hover:text-destructive"
          disabled={busy}
          onClick={() => {
            remove.reset()
            setRemoveOpen(true)
          }}
        >
          {t("credentials.keyPools.removeKey")}
        </Button>
      </div>
      {actionError && (
        <p role="alert" className="text-destructive text-sm">
          {actionError.message}
        </p>
      )}
      <ConfirmDialog
        open={removeOpen}
        onOpenChange={setRemoveOpen}
        title={t("credentials.keyPools.removeKeyTitle", {
          name: poolKey.label,
        })}
        description={t("credentials.keyPools.removeKeyDescription")}
        confirmLabel={t("credentials.keyPools.removeKey")}
        pending={remove.isPending}
        error={remove.error}
        onConfirm={() => remove.mutate()}
      />
    </li>
  )
}
