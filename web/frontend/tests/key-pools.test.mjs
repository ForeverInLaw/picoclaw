import assert from "node:assert/strict"
import { test } from "node:test"

test("Key pool management addresses the exact pool and key", async () => {
  const {
    addPoolKey,
    createKeyPool,
    deleteKeyPool,
    removePoolKey,
    renameKeyPool,
    returnPoolKeyToService,
    updatePoolKey,
  } = await import("../src/api/key-pools.ts")
  const originalFetch = globalThis.fetch
  const requests = []
  globalThis.fetch = async (path, options) => {
    requests.push({ path, ...options })
    return Response.json({ status: "success" })
  }
  try {
    await createKeyPool("Organizations")
    await renameKeyPool("orgs/1", "Work organizations")
    await addPoolKey("orgs/1", "First org", "sk-ant-api03-secret")
    await updatePoolKey("orgs/1", "first", { enabled: false })
    await updatePoolKey("orgs/1", "first", {
      monthly_budget_usd: 200,
      reset_day: 15,
    })
    await returnPoolKeyToService("orgs/1", "first")
    await removePoolKey("orgs/1", "first")
    await deleteKeyPool("orgs/1")
    assert.deepEqual(
      requests.map(({ path, method, body }) => ({ path, method, body })),
      [
        {
          path: "/api/key-pools",
          method: "POST",
          body: '{"name":"Organizations"}',
        },
        {
          path: "/api/key-pools/orgs%2F1",
          method: "PATCH",
          body: '{"name":"Work organizations"}',
        },
        {
          path: "/api/key-pools/orgs%2F1/keys",
          method: "POST",
          body: '{"label":"First org","api_key":"sk-ant-api03-secret"}',
        },
        {
          path: "/api/key-pools/orgs%2F1/keys/first",
          method: "PATCH",
          body: '{"enabled":false}',
        },
        {
          path: "/api/key-pools/orgs%2F1/keys/first",
          method: "PATCH",
          body: '{"monthly_budget_usd":200,"reset_day":15}',
        },
        {
          path: "/api/key-pools/orgs%2F1/keys/first/return",
          method: "POST",
          body: undefined,
        },
        {
          path: "/api/key-pools/orgs%2F1/keys/first",
          method: "DELETE",
          body: undefined,
        },
        { path: "/api/key-pools/orgs%2F1", method: "DELETE", body: undefined },
      ],
    )
    globalThis.fetch = async () =>
      Response.json(
        {
          message: "Choose other credentials for the listed models.",
          models: ["Opus", "Haiku"],
        },
        { status: 409 },
      )
    await assert.rejects(deleteKeyPool("orgs/1"), /listed models\. Opus, Haiku/)
  } finally {
    globalThis.fetch = originalFetch
  }
})

test("a model form's Key pool applies only to anthropic/ API-key models", async () => {
  const { selectedKeyPool } = await import("../src/api/key-pools.ts")
  assert.equal(selectedKeyPool("anthropic/claude-opus-5-5", "", "orgs"), "orgs")
  assert.equal(
    selectedKeyPool("anthropic/claude-opus-5-5", "oauth", "orgs"),
    "",
  )
  assert.equal(
    selectedKeyPool("anthropic/claude-opus-5-5", "token", "orgs"),
    "",
  )
  assert.equal(selectedKeyPool("openai/gpt-5", "", "orgs"), "")
})
