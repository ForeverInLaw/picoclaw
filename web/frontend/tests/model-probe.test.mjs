import assert from "node:assert/strict"
import { once } from "node:events"
import { createServer } from "node:http"
import { test } from "node:test"

test("model validation and inference are explicit distinct actions with selected edit identity", async () => {
  const { validateModelConfiguration, testModelConfiguration } =
    await import("../src/api/model-probe.ts")
  const requests = []
  const server = createServer(async (request, response) => {
    let body = ""
    for await (const part of request) body += part
    requests.push({
      path: request.url,
      method: request.method,
      body: JSON.parse(body),
    })
    response.writeHead(200, { "Content-Type": "application/json" }).end(
      JSON.stringify({
        status: request.url.endsWith("validate") ? "valid" : "success",
        message: "fixture result",
      }),
    )
  })
  server.listen(0, "127.0.0.1")
  await once(server, "listening")
  const originalFetch = globalThis.fetch
  globalThis.fetch = (path, options) =>
    originalFetch(
      new URL(path, `http://127.0.0.1:${server.address().port}`),
      options,
    )
  try {
    assert.equal(requests.length, 0)
    const selection = {
      model_index: 3,
      model_name: "selected",
      model: "openai/selected",
      api_base: "https://edited.example/v1",
      api_key: "",
      request_timeout: 2,
    }
    assert.equal((await validateModelConfiguration(selection)).status, "valid")
    assert.equal((await testModelConfiguration(selection)).status, "success")
    assert.deepEqual(requests, [
      { path: "/api/models/validate", method: "POST", body: selection },
      { path: "/api/models/test-inline", method: "POST", body: selection },
    ])
  } finally {
    globalThis.fetch = originalFetch
    await new Promise((resolve) => server.close(resolve))
  }
})

test("pool-backed models list Anthropic's models through the exact pool", async () => {
  const { getPoolModels } = await import("../src/api/key-pools.ts")
  const originalFetch = globalThis.fetch
  const requests = []
  globalThis.fetch = async (path, options) => {
    requests.push({ path, method: options?.method })
    return Response.json({
      models: [{ id: "claude-opus-5-5", display_name: "Claude Opus 5.5" }],
    })
  }
  try {
    assert.deepEqual(await getPoolModels("orgs/1"), [
      { id: "claude-opus-5-5", display_name: "Claude Opus 5.5" },
    ])
    assert.deepEqual(requests, [
      { path: "/api/key-pools/orgs%2F1/models", method: "GET" },
    ])
    globalThis.fetch = async () =>
      Response.json(
        { status: "error", message: "Anthropic did not list models." },
        { status: 502 },
      )
    await assert.rejects(getPoolModels("orgs"), /did not list models/)
  } finally {
    globalThis.fetch = originalFetch
  }
})
