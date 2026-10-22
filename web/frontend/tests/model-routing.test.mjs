import assert from "node:assert/strict"
import { test } from "node:test"

test("routing actions preserve scoped inheritance, empty reserves and durable model selections", async () => {
  const { getModelRouting, saveModelRouting } =
    await import("../src/api/model-routing.ts")
  const { modelReference, setDefaultModel, updateModel, deleteModel } =
    await import("../src/api/models.ts")
  const requests = []
  const originalFetch = globalThis.fetch
  globalThis.fetch = async (path, options) => {
    requests.push({
      path,
      method: options?.method || "GET",
      body: options?.body ? JSON.parse(options.body) : undefined,
    })
    return new Response(
      JSON.stringify({
        defaults: { primary: "model-id:entry-B", fallbacks: [] },
        agents: { worker: null },
        status: "ok",
      }),
      { headers: { "Content-Type": "application/json" } },
    )
  }
  try {
    assert.deepEqual((await getModelRouting()).agents, { worker: null })
    await saveModelRouting({
      agents: { worker: { primary: "", fallbacks: [] } },
    })
    await saveModelRouting({ agents: { worker: null } })
    const selected = { id: "entry-B", model_name: "duplicate alias" }
    await setDefaultModel(modelReference(selected))
    await updateModel(selected.id, { id: selected.id, model_name: "renamed" })
    await deleteModel(selected.id)
    assert.deepEqual(requests, [
      { path: "/api/models/routing", method: "GET", body: undefined },
      {
        path: "/api/models/routing",
        method: "PUT",
        body: { agents: { worker: { primary: "", fallbacks: [] } } },
      },
      {
        path: "/api/models/routing",
        method: "PUT",
        body: { agents: { worker: null } },
      },
      {
        path: "/api/models/default",
        method: "POST",
        body: { model_name: "model-id:entry-B" },
      },
      {
        path: "/api/models/by-id/entry-B",
        method: "PUT",
        body: { id: "entry-B", model_name: "renamed" },
      },
      { path: "/api/models/by-id/entry-B", method: "DELETE", body: undefined },
    ])
  } finally {
    globalThis.fetch = originalFetch
  }
})
