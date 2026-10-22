export interface CodexAccount {
  id: string
  label: string
  provider: "openai"
  enabled: boolean
  account_id?: string
  email?: string
  status: "authenticated" | "reauthorization_required" | "disabled"
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
