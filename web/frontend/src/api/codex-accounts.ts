export interface CodexAccount {
  id: string
  label: string
  provider: "openai"
  enabled: boolean
  account_id?: string
  email?: string
  status:
    | "authenticated"
    | "reauthorization_required"
    | "disabled"
    | "plan_permission_required"
  last_probe?: CodexAccountProbe
  last_success_at?: string
  expires_at?: string
  models: string[]
}

export async function getCodexAccounts(): Promise<{
  accounts: CodexAccount[]
}> {
  const response = await fetch("/api/oauth/accounts")
  if (!response.ok) throw new Error("Could not load Codex accounts.")
  return response.json() as Promise<{ accounts: CodexAccount[] }>
}

export interface CodexAccountProbe {
  at: string
  model: string
  status:
    | "success"
    | "failed"
    | "temporarily_unavailable"
    | "reauthorization_required"
  latency_ms: number
}

async function accountAction(
  accountID: string,
  method: "PATCH" | "DELETE" | "POST",
  body?: { label?: string; enabled?: boolean; model?: string },
) {
  const path = `/api/oauth/accounts/${encodeURIComponent(accountID)}${method === "POST" ? "/test" : ""}`
  const response = await fetch(path, {
    method,
    headers: body ? { "Content-Type": "application/json" } : undefined,
    body: body ? JSON.stringify(body) : undefined,
  })
  if (!response.ok) {
    const result: { message?: string; models?: string[] } = await response
      .json()
      .catch(() => ({}))
    const references = result.models?.length
      ? ` ${result.models.join(", ")}`
      : ""
    throw new Error(
      `${result.message || "Could not update Codex account."}${references}`,
    )
  }
}

export function updateCodexAccount(
  accountID: string,
  patch: { label?: string; enabled?: boolean },
) {
  return accountAction(accountID, "PATCH", patch)
}

export function deleteCodexAccount(accountID: string) {
  return accountAction(accountID, "DELETE")
}

export function testCodexAccount(accountID: string, model: string) {
  return accountAction(accountID, "POST", { model })
}
