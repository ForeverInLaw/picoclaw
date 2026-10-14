export interface SkillSupportItem {
  name: string
  path: string
  source: "workspace" | "global" | "builtin" | string
  description: string
}

export interface SkillDetailResponse extends SkillSupportItem {
  content: string
}

interface SkillsResponse {
  skills: SkillSupportItem[]
}

interface SkillActionResponse {
  status?: string
  name?: string
  path?: string
  source?: string
  description?: string
}

async function request<T>(path: string, options?: RequestInit): Promise<T> {
  const res = await fetch(path, options)
  if (!res.ok) {
    throw new Error(await extractErrorMessage(res))
  }
  return res.json() as Promise<T>
}

export async function getSkills(): Promise<SkillsResponse> {
  return request<SkillsResponse>("/api/skills")
}

export async function getSkill(name: string): Promise<SkillDetailResponse> {
  return request<SkillDetailResponse>(`/api/skills/${encodeURIComponent(name)}`)
}

export async function importSkill(file: File): Promise<SkillActionResponse> {
  const formData = new FormData()
  formData.set("file", file)

  const res = await fetch("/api/skills/import", {
    method: "POST",
    body: formData,
  })
  if (!res.ok) {
    throw new Error(await extractErrorMessage(res))
  }
  return res.json() as Promise<SkillActionResponse>
}

export async function deleteSkill(name: string): Promise<SkillActionResponse> {
  return request<SkillActionResponse>(
    `/api/skills/${encodeURIComponent(name)}`,
    {
      method: "DELETE",
    },
  )
}

async function extractErrorMessage(res: Response): Promise<string> {
  const text = await res.text()
  try {
    const body = JSON.parse(text) as {
      error?: string
      errors?: string[]
    }
    if (Array.isArray(body.errors) && body.errors.length > 0) {
      return body.errors.join("; ")
    }
    if (typeof body.error === "string" && body.error.trim() !== "") {
      return body.error
    }
  } catch {
    // ignore invalid body
  }
  return text.trim() || `API error: ${res.status} ${res.statusText}`
}

export interface HubSkillIdentity {
  registry: string
  slug: string
}

export interface HubSkillSearchResult {
  score: number
  slug: string
  display_name: string
  summary: string
  version: string
  registry_name: string
  installed: boolean
}

export interface HubSkillPage {
  results: HubSkillSearchResult[]
  has_more: boolean
  next_offset?: number
}

export interface HubSkillDetail {
  slug: string
  display_name: string
  summary: string
  latest_version: string
  is_malware_blocked: boolean
  is_suspicious: boolean
  registry_name: string
}

export interface HubInstallRequest extends HubSkillIdentity {
  version?: string
}

export interface HubInstallResponse {
  slug: string
  version: string
  is_suspicious: boolean
}

export function searchSkillHub(
  query: string,
  offset: number,
  signal: AbortSignal,
): Promise<HubSkillPage> {
  const params = new URLSearchParams({
    q: query,
    limit: "20",
    offset: String(offset),
  })
  return request(`/api/skills/hub/search?${params}`, {
    signal,
    credentials: "same-origin",
  })
}

export function getHubSkill(
  skill: HubSkillIdentity,
  signal: AbortSignal,
): Promise<HubSkillDetail> {
  return request(
    `/api/skills/hub/${encodeURIComponent(skill.registry)}/${encodeURIComponent(skill.slug)}`,
    { signal, credentials: "same-origin" },
  )
}

export function installHubSkill(
  skill: HubInstallRequest,
  signal: AbortSignal,
): Promise<HubInstallResponse> {
  return request("/api/skills/hub/install", {
    method: "POST",
    signal,
    credentials: "same-origin",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(skill),
  })
}
