export interface RuntimeCandidate {
  id?: string
  name?: string
  provider: string
  model: string
  account_id?: string
}

export interface RuntimeAttempt {
  at: string
  phase: string
  candidate: RuntimeCandidate
  reason?: string
  http_status?: number
  duration_ms?: number
  inactivity_timeout_ms?: number
  next_eligible_at?: string
}

export interface RuntimeRecord {
  id: string
  parent_id?: string
  agent_id: string
  kind: string
  started_at: string
  finished_at?: string
  state: string
  chain: RuntimeCandidate[]
  selected?: RuntimeCandidate
  attempts: RuntimeAttempt[]
  total_attempts: number
  truncated?: boolean
}

export interface ModelRuntime {
  state: "running" | "offline" | "stale" | "unknown"
  runtime: {
    updated_at?: string
    dropped_records?: number
    records: RuntimeRecord[]
    availability: {
      candidate: RuntimeCandidate
      available: boolean
      reason?: string
      authorization?: string
      next_eligible_at?: string
    }[]
  }
}

export async function getModelRuntime(): Promise<ModelRuntime> {
  const response = await fetch("/api/models/runtime")
  if (!response.ok) throw new Error("Could not load model runtime status.")
  return response.json() as Promise<ModelRuntime>
}
