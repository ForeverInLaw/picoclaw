import { useQuery } from "@tanstack/react-query"
import { useTranslation } from "react-i18next"

import { getCodexAccounts } from "@/api/codex-accounts"
import { type RuntimeCandidate, getModelRuntime } from "@/api/model-runtime"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/components/ui/collapsible"

function candidateName(candidate: RuntimeCandidate) {
  const name = candidate.name || candidate.model
  return candidate.id ? `${name} · ${candidate.id.slice(0, 8)}` : name
}

export function RuntimeRoutingStatus() {
  const { t } = useTranslation()
  const status = useQuery({
    queryKey: ["model-runtime"],
    queryFn: getModelRuntime,
    refetchInterval: 5000,
  })
  const accounts = useQuery({
    queryKey: ["codex-accounts"],
    queryFn: getCodexAccounts,
  })
  const translateState = (state: string) =>
    t(`models.runtime.states.${state}`, {
      defaultValue: t("models.runtime.states.unknown"),
    })
  const reason = (value?: string) =>
    value
      ? t(`models.runtime.reasons.${value}`, {
          defaultValue: t("models.runtime.reasons.unknown"),
        })
      : ""
  const connection = (id?: string) => {
    if (!id) return null
    const account = accounts.data?.accounts.find((saved) => saved.id === id)
    return (
      <span className="break-all">
        {account?.label ? `${account.label} · ` : ""}
        {id}
      </span>
    )
  }

  return (
    <Card className="my-6">
      <CardHeader>
        <CardTitle>{t("models.runtime.title")}</CardTitle>
        <CardDescription>{t("models.runtime.description")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        {status.isPending ? (
          <p>{t("models.runtime.loading")}</p>
        ) : status.error ? (
          <p role="alert">{status.error.message}</p>
        ) : status.data ? (
          <>
            <div className="flex flex-wrap items-center gap-2">
              <Badge variant="secondary">
                {translateState(status.data.state)}
              </Badge>
              {status.data.runtime.updated_at && (
                <span>
                  {t("models.runtime.updated")}:{" "}
                  {new Date(status.data.runtime.updated_at).toLocaleString()}
                </span>
              )}
            </div>
            {status.data.state !== "running" && (
              <p>{t("models.runtime.unavailable")}</p>
            )}
            <ul className="flex flex-col gap-3">
              {(status.data.runtime.availability ?? []).map((item, index) => {
                const account = accounts.data?.accounts.find(
                  (saved) => saved.id === item.candidate.account_id,
                )
                return (
                  <li
                    key={`${item.candidate.id}-${index}`}
                    className="flex flex-col gap-1"
                  >
                    <div className="flex flex-wrap items-center gap-2">
                      <span>{candidateName(item.candidate)}</span>
                      <Badge variant="outline">
                        {item.available
                          ? t("models.runtime.eligible")
                          : reason(item.reason)}
                      </Badge>
                      {item.authorization && (
                        <span>
                          {t(`credentials.accounts.${item.authorization}`, {
                            defaultValue: item.authorization,
                          })}
                        </span>
                      )}
                    </div>
                    {connection(item.candidate.account_id)}
                    {item.next_eligible_at ? (
                      <p>
                        {t("models.runtime.nextAttempt")}:{" "}
                        {new Date(item.next_eligible_at).toLocaleString()}
                      </p>
                    ) : item.reason === "billing" ? (
                      <p>{t("models.runtime.unknownReset")}</p>
                    ) : null}
                    {account && (
                      <p>
                        {t("credentials.accounts.lastSuccess")}:{" "}
                        {account.last_success_at
                          ? new Date(account.last_success_at).toLocaleString()
                          : t("credentials.accounts.neverChecked")}
                      </p>
                    )}
                  </li>
                )
              })}
            </ul>
            <p>{t("models.runtime.recent")}</p>
            {!(status.data.runtime.records ?? []).length && (
              <p>{t("models.runtime.empty")}</p>
            )}
            <ul className="flex flex-col gap-4">
              {[...(status.data.runtime.records ?? [])]
                .reverse()
                .slice(0, 12)
                .map((record) => (
                  <li key={record.id} className="flex flex-col gap-2">
                    <div className="flex flex-wrap items-center gap-2">
                      <span>
                        {record.agent_id || t("models.runtime.internal")} ·{" "}
                        {translateState(record.kind)}
                      </span>
                      <Badge variant="secondary">
                        {translateState(record.state)}
                      </Badge>
                      <span>
                        {new Date(record.started_at).toLocaleString()}
                      </span>
                    </div>
                    <p>
                      {t("models.runtime.chain")}:{" "}
                      {record.chain.map(candidateName).join(" → ") || "—"}
                    </p>
                    {record.selected && (
                      <p>
                        {t("models.runtime.answered")}:{" "}
                        {candidateName(record.selected)} ·{" "}
                        {record.selected.model}{" "}
                        {connection(record.selected.account_id)}
                      </p>
                    )}
                    <Collapsible>
                      <CollapsibleTrigger asChild>
                        <Button variant="ghost" size="sm">
                          {t("models.runtime.attempts", {
                            count: record.total_attempts,
                          })}
                        </Button>
                      </CollapsibleTrigger>
                      <CollapsibleContent>
                        <ul className="flex flex-col gap-1">
                          {record.attempts
                            .filter(
                              (attempt) =>
                                attempt.phase !== "attempt" &&
                                attempt.phase !== "success",
                            )
                            .map((attempt, index) => (
                              <li key={index}>
                                {attempt.candidate.model && (
                                  <>{candidateName(attempt.candidate)} · </>
                                )}
                                {translateState(attempt.phase)} ·{" "}
                                {reason(attempt.reason)}
 {attempt.http_status ? ` (HTTP ${attempt.http_status})` : ""}
                                {attempt.inactivity_timeout_ms
                                  ? ` (${attempt.inactivity_timeout_ms / 1000}s)`
                                  : ""}
                                {attempt.next_eligible_at && (
                                  <>
                                    {" "}
                                    · {t("models.runtime.nextAttempt")}:{" "}
                                    {new Date(
                                      attempt.next_eligible_at,
                                    ).toLocaleString()}
                                  </>
                                )}
                              </li>
                            ))}
                        </ul>
                      </CollapsibleContent>
                    </Collapsible>
                    {record.truncated && <p>{t("models.runtime.truncated")}</p>}
                  </li>
                ))}
            </ul>
            {!!status.data.runtime.dropped_records && (
              <p>{t("models.runtime.truncated")}</p>
            )}
          </>
        ) : null}
      </CardContent>
    </Card>
  )
}
