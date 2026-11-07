export type KeyPoolKeyState =
  | "available"
  | "rate_limited"
  | "exhausted"
  | "rejected"
  | "disabled"
  | "unknown"

export interface ModelRateLimit {
  model: string
  until: string
}

// One key of a Key pool. api_key is always masked by the server.
export interface KeyPoolKey {
  id: string
  label: string
  enabled: boolean
  api_key: string
  state: KeyPoolKeyState
  until?: string
  rate_limited_models?: ModelRateLimit[]
}

export interface KeyPool {
  id: string
  name: string
  models: string[]
  keys: KeyPoolKey[]
}

export interface KeyPoolListing {
  runtime: "running" | "stale" | "offline" | "unknown"
  pools: KeyPool[]
}

// States a key leaves only through Return to service or a successful recheck.
export const keyStatesOutOfService: ReadonlySet<KeyPoolKeyState> = new Set([
  "rate_limited",
  "exhausted",
  "rejected",
])

export const ANTHROPIC_PREFIX = "anthropic/"

// Key pools serve only anthropic/ models that authenticate with API keys.
export function canUseKeyPool(model: string, authMethod: string) {
  return (
    model.startsWith(ANTHROPIC_PREFIX) &&
    authMethod !== "oauth" &&
    authMethod !== "token"
  )
}

const poolPath = (poolID: string) =>
  `/api/key-pools/${encodeURIComponent(poolID)}`
const keyPath = (poolID: string, keyID: string) =>
  `${poolPath(poolID)}/keys/${encodeURIComponent(keyID)}`

async function request<T>(
  path: string,
  method: "GET" | "POST" | "PATCH" | "DELETE",
  body?: object,
): Promise<T> {
  const response = await fetch(
    path,
    body
      ? {
          method,
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(body),
        }
      : { method },
  )
  if (!response.ok) {
    const result: { message?: string; models?: string[] } = await response
      .json()
      .catch(() => ({}))
    const references = result.models?.length
      ? ` ${result.models.join(", ")}`
      : ""
    throw new Error(
      `${result.message || "Could not update Key pools."}${references}`,
    )
  }
  return response.json() as Promise<T>
}

export function getKeyPools() {
  return request<KeyPoolListing>("/api/key-pools", "GET")
}

export function createKeyPool(name: string) {
  return request<KeyPool>("/api/key-pools", "POST", { name })
}

export function renameKeyPool(poolID: string, name: string) {
  return request(poolPath(poolID), "PATCH", { name })
}

export function deleteKeyPool(poolID: string) {
  return request(poolPath(poolID), "DELETE")
}

// The server checks the key with Anthropic before saving it.
export function addPoolKey(poolID: string, label: string, apiKey: string) {
  return request<KeyPoolKey>(`${poolPath(poolID)}/keys`, "POST", {
    label,
    api_key: apiKey,
  })
}

export function updatePoolKey(
  poolID: string,
  keyID: string,
  patch: { label?: string; enabled?: boolean },
) {
  return request(keyPath(poolID, keyID), "PATCH", patch)
}

export function removePoolKey(poolID: string, keyID: string) {
  return request(keyPath(poolID, keyID), "DELETE")
}

export function returnPoolKeyToService(poolID: string, keyID: string) {
  return request(`${keyPath(poolID, keyID)}/return`, "POST")
}

// One model Anthropic offers to a Key pool's keys.
export interface PoolModel {
  id: string
  display_name: string
}

// Lists the models Anthropic offers through the pool's first working key.
export async function getPoolModels(poolID: string) {
  const { models } = await request<{ models: PoolModel[] }>(
    `${poolPath(poolID)}/models`,
    "GET",
  )
  return models
}
