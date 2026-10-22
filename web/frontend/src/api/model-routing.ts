export interface RoutingChoice {
  primary: string
  fallbacks: string[] | null
}

export interface ModelRouting {
  defaults: RoutingChoice
  agents: Record<string, RoutingChoice | null>
}

export async function getModelRouting(): Promise<ModelRouting> {
  const response = await fetch("/api/models/routing")
  if (!response.ok) throw new Error("Could not load model order.")
  return response.json() as Promise<ModelRouting>
}

export async function saveModelRouting(
  routing: Partial<ModelRouting>,
): Promise<ModelRouting> {
  const response = await fetch("/api/models/routing", {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(routing),
  })
  if (!response.ok)
    throw new Error((await response.text()) || "Could not save model order.")
  return response.json() as Promise<ModelRouting>
}
