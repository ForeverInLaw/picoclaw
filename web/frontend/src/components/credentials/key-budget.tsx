import type { TFunction } from "i18next"
import { useTranslation } from "react-i18next"

import type { KeyPoolKey, KeyPoolKeyPatch } from "@/api/key-pools"
import { Input } from "@/components/ui/input"

import { InlineEdit } from "./inline-edit"

const dollars = (value: number) =>
  value.toLocaleString(undefined, {
    style: "currency",
    currency: "USD",
    minimumFractionDigits: Number.isInteger(value) ? 0 : 2,
  })

// The facts of a key's current budget period: estimated spend against its
// budget, tokens of models without a price, cache hit rate and period start.
function spendFacts(poolKey: KeyPoolKey, t: TFunction) {
  const { spend, monthly_budget_usd: budget } = poolKey
  const facts: string[] = []
  if (spend.estimated_usd > 0 || spend.unpriced_tokens === 0) {
    const amount = dollars(spend.estimated_usd)
    facts.push(
      budget
        ? t("credentials.keyPools.spendOfBudget", {
            amount,
            budget: dollars(budget),
          })
        : t("credentials.keyPools.spend", { amount }),
    )
  }
  if (spend.unpriced_tokens > 0)
    facts.push(
      t("credentials.keyPools.unpricedTokens", {
        tokens: spend.unpriced_tokens.toLocaleString(undefined, {
          notation: "compact",
        }),
      }),
    )
  if (spend.input_tokens + spend.cache_write_tokens + spend.cache_read_tokens)
    facts.push(
      t("credentials.keyPools.cacheHitRate", {
        rate: spend.cache_hit_rate.toLocaleString(undefined, {
          style: "percent",
        }),
      }),
    )
  facts.push(
    t("credentials.keyPools.since", {
      date: new Date(spend.since).toLocaleDateString(undefined, {
        month: "short",
        day: "numeric",
        timeZone: "UTC",
      }),
    }),
  )
  return facts.join(" · ")
}

// A key's spend in its budget period, with a Budget action that edits the
// monthly budget and reset day in place.
export function KeyBudget({
  poolKey,
  disabled,
  onSave,
}: {
  poolKey: KeyPoolKey
  disabled: boolean
  onSave: (patch: KeyPoolKeyPatch) => Promise<unknown>
}) {
  const { t } = useTranslation()
  return (
    <InlineEdit
      view={<p className="text-sm">{spendFacts(poolKey, t)}</p>}
      actionLabel={t("credentials.keyPools.budget")}
      disabled={disabled}
      formClassName="flex flex-wrap items-end gap-2"
      onSubmit={(form) =>
        onSave({
          monthly_budget_usd: Number(form.get("budget") || 0),
          reset_day: Number(form.get("resetDay")),
        })
      }
    >
      <label className="flex flex-col gap-1 text-sm">
        {t("credentials.keyPools.monthlyBudget")}
        <Input
          className="w-36"
          name="budget"
          type="number"
          min={0}
          max={1_000_000}
          step={0.01}
          defaultValue={poolKey.monthly_budget_usd ?? ""}
          autoFocus
          disabled={disabled}
        />
      </label>
      <label className="flex flex-col gap-1 text-sm">
        {t("credentials.keyPools.resetDay")}
        <Input
          className="w-24"
          name="resetDay"
          type="number"
          min={1}
          max={31}
          step={1}
          defaultValue={poolKey.reset_day}
          required
          disabled={disabled}
        />
      </label>
    </InlineEdit>
  )
}
