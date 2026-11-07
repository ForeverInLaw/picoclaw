import { IconLoader2 } from "@tabler/icons-react"
import { useMutation } from "@tanstack/react-query"
import { useEffect, useRef } from "react"
import { useTranslation } from "react-i18next"

import {
  type ModelTestConfiguration,
  testModelConfiguration,
  validateModelConfiguration,
} from "@/api/model-probe"
import { Button } from "@/components/ui/button"
import { FieldDescription, FieldLegend, FieldSet } from "@/components/ui/field"

export function ModelProbe({
  configuration,
}: {
  configuration: ModelTestConfiguration
}) {
  const { t } = useTranslation()
  const controllerRef = useRef<AbortController | null>(null)
  const probe = useMutation({
    mutationFn: ({
      mode,
      controller,
    }: {
      mode: "validate" | "test"
      controller: AbortController
    }) =>
      mode === "validate"
        ? validateModelConfiguration(configuration, controller.signal)
        : testModelConfiguration(configuration, controller.signal),
    retry: false,
  })
  useEffect(() => () => controllerRef.current?.abort(), [])

  const run = (mode: "validate" | "test") => {
    const controller = new AbortController()
    controllerRef.current = controller
    probe.mutate({ mode, controller })
  }
  const canceled = probe.variables?.controller.signal.aborted

  return (
    <FieldSet className="gap-3">
      <FieldLegend>{t("models.probe.title")}</FieldLegend>
      <FieldDescription>{t("models.probe.description")}</FieldDescription>
      <div className="flex flex-wrap gap-2">
        <Button
          variant="outline"
          size="sm"
          disabled={probe.isPending}
          onClick={() => run("validate")}
        >
          {t("models.probe.validate")}
        </Button>
        <Button
          variant="secondary"
          size="sm"
          disabled={probe.isPending}
          onClick={() => run("test")}
        >
          {probe.isPending && (
            <IconLoader2 data-icon="inline-start" className="animate-spin" />
          )}
          {t("models.probe.test")}
        </Button>
        {probe.isPending && (
          <Button
            variant="ghost"
            size="sm"
            onClick={() => controllerRef.current?.abort()}
          >
            {t("common.cancel")}
          </Button>
        )}
      </div>
      <p
        role="status"
        aria-live="polite"
        className="text-muted-foreground text-sm"
      >
        {probe.isPending
          ? probe.variables?.mode === "validate"
            ? t("models.probe.validating")
            : t("models.probe.testing")
          : probe.isError
            ? canceled
              ? t("models.probe.canceled")
              : probe.error.message
            : probe.data?.keys
              ? t("models.probe.keysReplied", {
                  replied: probe.data.keys.filter(
                    ({ status }) => status === "success",
                  ).length,
                  total: probe.data.keys.length,
                })
              : probe.data?.status === "valid"
                ? t("models.probe.valid")
                : probe.data?.status === "success"
                  ? t("models.probe.success", {
                      latency: probe.data.latency_ms,
                    })
                  : null}
      </p>
      {!probe.isPending && probe.data?.keys && (
        <ul className="flex flex-col gap-1 text-sm">
          {probe.data.keys.map((key, index) => (
            <li key={index} className="flex flex-wrap gap-x-2">
              <span className="font-medium">{key.label}</span>
              <span
                className={
                  key.status === "success"
                    ? "text-muted-foreground"
                    : "text-destructive"
                }
              >
                {key.status === "success"
                  ? t("models.probe.keySuccess", { latency: key.latency_ms })
                  : key.message}
              </span>
            </li>
          ))}
        </ul>
      )}
    </FieldSet>
  )
}
