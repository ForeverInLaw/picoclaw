import type { ModelInfo } from "./models"

export type ModelTestConfiguration = Omit<
  ModelInfo,
  "id" | "index" | "configured" | "is_default" | "api_key"
> & { id?: string; api_key?: string; model_index?: number }

// Test model's outcome on one Key pool key, named by its label only.
export interface KeyTestResult {
  label: string
  status: "success" | "error"
  message: string
  latency_ms: number
}

// A pool-backed model is tested on each available key; its status is
// "failed" unless every key replied.
export interface ModelTestResult {
  status: "valid" | "success" | "failed"
  message: string
  latency_ms?: number
  keys?: KeyTestResult[]
}

async function modelTestRequest(
  path: string,
  configuration: ModelTestConfiguration,
  signal?: AbortSignal,
): Promise<ModelTestResult> {
  const response = await fetch(path, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(configuration),
    signal,
  })
  const result: unknown = await response.json().catch(() => null)
  if (!response.ok) {
    const message =
      result &&
      typeof result === "object" &&
      "message" in result &&
      typeof result.message === "string"
        ? result.message
        : response.status === 401
          ? "Sign in to test this model."
          : "The model test could not complete. Try again."
    throw new Error(message)
  }
  return result as ModelTestResult
}

export function validateModelConfiguration(
  configuration: ModelTestConfiguration,
  signal?: AbortSignal,
) {
  return modelTestRequest("/api/models/validate", configuration, signal)
}

export function testModelConfiguration(
  configuration: ModelTestConfiguration,
  signal?: AbortSignal,
) {
  return modelTestRequest("/api/models/test-inline", configuration, signal)
}
