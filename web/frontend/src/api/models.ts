// API client for model list management.

export interface ModelInfo {
  id: string
  index: number
  model_name: string
  model: string
  api_base?: string
  api_key: string
  proxy?: string
  auth_method?: string
  account_id?: string
  capabilities?: { images?: boolean; tools?: boolean; context_window?: number }
  api_keys?: string[]
  fallbacks?: string[] | null
  // Advanced fields
  connect_mode?: string
  workspace?: string
  rpm?: number
  max_tokens_field?: string
  request_timeout?: number
  inactivity_timeout_seconds?: number
  thinking_level?: string
  extra_body?: Record<string, unknown>
  // Meta
  configured: boolean
  is_default: boolean
}

interface ModelsListResponse {
  models: ModelInfo[]
  total: number
  default_model: string
}

interface ModelActionResponse {
  status: string
  index?: number
  id?: string
  default_model?: string
}

const BASE_URL = ""

async function request<T>(path: string, options?: RequestInit): Promise<T> {
  const res = await fetch(`${BASE_URL}${path}`, options)
  if (!res.ok) {
    const text = await res.text()
    throw new Error(text || `API error: ${res.status} ${res.statusText}`)
  }
  return res.json() as Promise<T>
}

export async function getModels(): Promise<ModelsListResponse> {
  return request<ModelsListResponse>("/api/models")
}

export async function addModel(
  model: Partial<ModelInfo>,
): Promise<ModelActionResponse> {
  return request<ModelActionResponse>("/api/models", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(model),
  })
}

export async function updateModel(
  id: string,
  model: Partial<ModelInfo>,
): Promise<ModelActionResponse> {
  return request<ModelActionResponse>(
    `/api/models/by-id/${encodeURIComponent(id)}`,
    {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(model),
    },
  )
}

export async function deleteModel(id: string): Promise<ModelActionResponse> {
  return request<ModelActionResponse>(
    `/api/models/by-id/${encodeURIComponent(id)}`,
    {
      method: "DELETE",
    },
  )
}

export async function setDefaultModel(
  modelName: string,
): Promise<ModelActionResponse> {
  const response = await request<ModelActionResponse>("/api/models/default", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ model_name: modelName }),
  })

  return response
}

export type { ModelsListResponse, ModelActionResponse }

export function modelReference(model: ModelInfo) {
  return `model-id:${model.id}`
}
