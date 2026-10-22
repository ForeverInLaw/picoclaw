import {
  IconArrowDown,
  IconArrowUp,
  IconPlus,
  IconX,
} from "@tabler/icons-react"
import { useMutation, useQuery } from "@tanstack/react-query"
import { useState } from "react"
import { useTranslation } from "react-i18next"

import { getCodexAccounts } from "@/api/codex-accounts"
import {
  type ModelRouting,
  type RoutingChoice,
  saveModelRouting,
} from "@/api/model-routing"
import { type ModelInfo, modelReference } from "@/api/models"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardAction,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import { Field, FieldGroup, FieldLabel } from "@/components/ui/field"
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetFooter,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet"
import { Switch } from "@/components/ui/switch"
import {
  modelRoutingQueryOptions,
  useInvalidateModels,
} from "@/hooks/use-models"

const DEFAULT_SCOPE = "__instance__"

export function ModelRoutingEditor({ models }: { models: ModelInfo[] }) {
  const { t } = useTranslation()
  const routing = useQuery(modelRoutingQueryOptions)
  const accounts = useQuery({
    queryKey: ["codex-accounts"],
    queryFn: getCodexAccounts,
  })
  const [open, setOpen] = useState(false)
  const [scope, setScope] = useState(DEFAULT_SCOPE)
  const choices = models.map((model) => {
    const account = accounts.data?.accounts.find(
      (item) => item.id === model.account_id,
    )
    const identity = account
      ? `${account.label} · ${account.id.slice(-8)}`
      : `${model.api_base || model.model} · ${model.id.slice(-8)}`
    return {
      value: modelReference(model),
      label: `${model.model_name} · ${identity}`,
    }
  })
  return (
    <>
      <Card size="sm" className="my-4">
        <CardHeader>
          <CardTitle>{t("models.routing.title")}</CardTitle>
          <CardDescription>{t("models.routing.description")}</CardDescription>
          <CardAction>
            <Button variant="outline" size="sm" onClick={() => setOpen(true)}>
              {t("models.routing.edit")}
            </Button>
          </CardAction>
        </CardHeader>
      </Card>
      <Sheet open={open} onOpenChange={setOpen}>
        <SheetContent className="overflow-y-auto sm:max-w-xl">
          <SheetHeader>
            <SheetTitle>{t("models.routing.title")}</SheetTitle>
            <SheetDescription>
              {t("models.routing.description")}
            </SheetDescription>
          </SheetHeader>
          <div className="px-4">
            <FieldGroup>
              <Field>
                <FieldLabel>{t("models.routing.scope")}</FieldLabel>
                <Select value={scope} onValueChange={setScope}>
                  <SelectTrigger
                    className="w-full"
                    aria-label={t("models.routing.scope")}
                  >
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectGroup>
                      <SelectItem value={DEFAULT_SCOPE}>
                        {t("models.routing.instance")}
                      </SelectItem>
                      {Object.keys(routing.data?.agents ?? {}).map((id) => (
                        <SelectItem key={id} value={id}>
                          {id}
                        </SelectItem>
                      ))}
                    </SelectGroup>
                  </SelectContent>
                </Select>
              </Field>
            </FieldGroup>
            {routing.error && <p role="alert">{routing.error.message}</p>}
            {routing.data && open && (
              <RoutingForm
                key={scope}
                routing={routing.data}
                scope={scope}
                choices={choices}
                onSaved={() => setOpen(false)}
              />
            )}
          </div>
        </SheetContent>
      </Sheet>
    </>
  )
}

interface RoutingFormProps {
  routing: ModelRouting
  scope: string
  choices: { value: string; label: string }[]
  onSaved: () => void
}
function RoutingForm({ routing, scope, choices, onSaved }: RoutingFormProps) {
  const { t } = useTranslation()
  const isDefault = scope === DEFAULT_SCOPE
  const initial = isDefault ? routing.defaults : routing.agents[scope]
  const [inherit, setInherit] = useState(!isDefault && initial == null)
  const [primary, setPrimary] = useState(initial?.primary || "")
  const [inheritReserves, setInheritReserves] = useState(
    initial?.fallbacks == null,
  )
  const [fallbacks, setFallbacks] = useState(initial?.fallbacks ?? [])
  const invalidate = useInvalidateModels()
  const save = useMutation({
    mutationFn: () => {
      const choice: RoutingChoice = {
        primary,
        fallbacks: inheritReserves ? null : fallbacks,
      }
      return saveModelRouting(
        isDefault
          ? { defaults: choice }
          : { agents: { [scope]: inherit ? null : choice } },
      )
    },
    onSuccess: async () => {
      await invalidate()
      onSaved()
    },
  })
  const move = (index: number, direction: -1 | 1) =>
    setFallbacks((current) => {
      const next = [...current]
      ;[next[index], next[index + direction]] = [
        next[index + direction],
        next[index],
      ]
      return next
    })
  const available = choices.filter(
    (choice) => choice.value !== primary && !fallbacks.includes(choice.value),
  )
  const selected = (
    value: string,
    change: (value: string) => void,
    label: string,
    allowInheritance = false,
  ) => (
    <Select
      value={value || (allowInheritance ? "__inherit_primary__" : undefined)}
      onValueChange={(next) =>
        change(next === "__inherit_primary__" ? "" : next)
      }
      disabled={inherit || save.isPending}
    >
      <SelectTrigger className="w-full min-w-0" aria-label={label}>
        <SelectValue placeholder={t("models.routing.choose")} />
      </SelectTrigger>
      <SelectContent>
        <SelectGroup>
          {allowInheritance && (
            <SelectItem value="__inherit_primary__">
              {t("models.routing.inheritedPrimary")}
            </SelectItem>
          )}
          {value && !choices.some((choice) => choice.value === value) && (
            <SelectItem value={value}>{value}</SelectItem>
          )}
          {choices.map((choice) => (
            <SelectItem key={choice.value} value={choice.value}>
              {choice.label}
            </SelectItem>
          ))}
        </SelectGroup>
      </SelectContent>
    </Select>
  )
  return (
    <form
      onSubmit={(event) => {
        event.preventDefault()
        save.mutate()
      }}
      className="mt-6"
    >
      <FieldGroup>
        {!isDefault && (
          <Field orientation="horizontal">
            <Switch
              id="inherit-routing"
              checked={inherit}
              onCheckedChange={setInherit}
              disabled={save.isPending}
            />
            <FieldLabel htmlFor="inherit-routing">
              {t("models.routing.inherit")}
            </FieldLabel>
          </Field>
        )}
        <Field>
          <FieldLabel>{t("models.routing.primary")}</FieldLabel>
          {selected(
            primary,
            setPrimary,
            t("models.routing.primary"),
            !isDefault,
          )}
        </Field>
        <Field orientation="horizontal">
          <Switch
            id="inherit-reserves"
            checked={inheritReserves}
            onCheckedChange={setInheritReserves}
            disabled={inherit || save.isPending}
          />
          <FieldLabel htmlFor="inherit-reserves">
            {t(
              isDefault
                ? "models.routing.modelReserves"
                : "models.routing.inheritedReserves",
            )}
          </FieldLabel>
        </Field>
        {!inherit && !inheritReserves && (
          <Field>
            <FieldLabel>{t("models.routing.reserves")}</FieldLabel>
            {fallbacks.length === 0 && (
              <p className="text-muted-foreground">
                {t("models.routing.noReserves")}
              </p>
            )}
            {fallbacks.map((reference, index) => (
              <div key={index} className="flex min-w-0 items-center gap-1">
                <span className="text-muted-foreground w-5 shrink-0">
                  {index + 1}.
                </span>
                {selected(
                  reference,
                  (next) =>
                    setFallbacks((current) =>
                      current.map((value, i) => (i === index ? next : value)),
                    ),
                  t("models.routing.reserve", { number: index + 1 }),
                )}
                <Button
                  type="button"
                  variant="ghost"
                  size="icon"
                  aria-label={t("models.routing.up")}
                  disabled={index === 0 || save.isPending}
                  onClick={() => move(index, -1)}
                >
                  <IconArrowUp />
                </Button>
                <Button
                  type="button"
                  variant="ghost"
                  size="icon"
                  aria-label={t("models.routing.down")}
                  disabled={index === fallbacks.length - 1 || save.isPending}
                  onClick={() => move(index, 1)}
                >
                  <IconArrowDown />
                </Button>
                <Button
                  type="button"
                  variant="ghost"
                  size="icon"
                  aria-label={t("models.routing.remove")}
                  disabled={save.isPending}
                  onClick={() =>
                    setFallbacks((current) =>
                      current.filter((_, i) => i !== index),
                    )
                  }
                >
                  <IconX />
                </Button>
              </div>
            ))}
            <Button
              type="button"
              variant="outline"
              disabled={available.length === 0 || save.isPending}
              onClick={() =>
                setFallbacks((current) => [...current, available[0].value])
              }
            >
              <IconPlus />
              {t("models.routing.addReserve")}
            </Button>
          </Field>
        )}
      </FieldGroup>
      {save.error && (
        <p role="alert" className="text-destructive mt-4">
          {save.error.message}
        </p>
      )}
      <SheetFooter className="px-0">
        <Button
          type="submit"
          disabled={save.isPending || (!inherit && isDefault && !primary)}
        >
          {save.isPending ? t("common.saving") : t("common.save")}
        </Button>
      </SheetFooter>
    </form>
  )
}
