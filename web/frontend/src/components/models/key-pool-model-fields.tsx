import { useQuery } from "@tanstack/react-query"
import { useTranslation } from "react-i18next"

import { ANTHROPIC_PREFIX } from "@/api/key-pools"
import { Field } from "@/components/shared-form"
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import {
  keyPoolsQueryOptions,
  poolModelsQueryOptions,
} from "@/hooks/use-key-pools"

const OWN_KEY = "own-key"

interface KeyPoolModelFieldsProps {
  poolID: string
  model: string
  onPoolChange: (poolID: string) => void
  onModelChange: (model: string) => void
}

// Lets an anthropic/ model use a Key pool instead of its own API key, then pick
// its model id from the list Anthropic returns for that pool. A failed list is
// shown as an error and keeps the current model selectable.
export function KeyPoolModelFields({
  poolID,
  model,
  onPoolChange,
  onModelChange,
}: KeyPoolModelFieldsProps) {
  const { t } = useTranslation()
  const pools = useQuery({ ...keyPoolsQueryOptions, refetchInterval: false })
  const models = useQuery({
    ...poolModelsQueryOptions(poolID),
    enabled: poolID !== "",
  })
  const current = model.slice(ANTHROPIC_PREFIX.length)
  const listed = models.data ?? []

  return (
    <>
      <Field label={t("models.keyPool.credentials")}>
        <Select
          value={poolID || OWN_KEY}
          onValueChange={(value) =>
            onPoolChange(value === OWN_KEY ? "" : value)
          }
        >
          <SelectTrigger
            aria-label={t("models.keyPool.credentials")}
            className="w-full"
          >
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectGroup>
              <SelectItem value={OWN_KEY}>
                {t("models.field.apiKey")}
              </SelectItem>
              {pools.data?.pools.map((pool) => (
                <SelectItem key={pool.id} value={pool.id}>
                  {t("models.keyPool.pool", { name: pool.name })}
                </SelectItem>
              ))}
            </SelectGroup>
          </SelectContent>
        </Select>
        {pools.error && (
          <p role="alert" className="text-destructive text-xs">
            {pools.error.message}
          </p>
        )}
      </Field>
      {poolID && (
        <Field label={t("models.keyPool.model")}>
          <Select
            value={current}
            onValueChange={(id) => onModelChange(ANTHROPIC_PREFIX + id)}
            disabled={models.isPending}
          >
            <SelectTrigger
              aria-label={t("models.keyPool.model")}
              className="w-full font-mono text-sm"
            >
              <SelectValue
                placeholder={
                  models.isPending
                    ? t("models.keyPool.loadingModels")
                    : t("models.keyPool.selectModel")
                }
              />
            </SelectTrigger>
            <SelectContent>
              <SelectGroup>
                {current && !listed.some(({ id }) => id === current) && (
                  <SelectItem value={current}>{current}</SelectItem>
                )}
                {listed.map(({ id, display_name }) => (
                  <SelectItem key={id} value={id}>
                    {display_name} · {id}
                  </SelectItem>
                ))}
              </SelectGroup>
            </SelectContent>
          </Select>
          {models.error && (
            <p role="alert" className="text-destructive text-xs">
              {models.error.message}
            </p>
          )}
        </Field>
      )}
    </>
  )
}
