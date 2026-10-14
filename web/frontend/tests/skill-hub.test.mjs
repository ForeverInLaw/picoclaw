import assert from "node:assert/strict"
import { once } from "node:events"
import { createServer } from "node:http"
import { test } from "node:test"

test("Hub API uses paginated search and registry identity without browser credentials", async () => {
  const { searchSkillHub, getHubSkill, installHubSkill } =
    await import("../src/api/skills.ts")
  const requests = []
  const server = createServer(async (request, response) => {
    let body = ""
    for await (const part of request) body += part
    requests.push({
      path: request.url,
      method: request.method,
      body,
      authorization: request.headers.authorization,
    })
    response
      .writeHead(200, { "Content-Type": "application/json" })
      .end(JSON.stringify({ results: [], has_more: false }))
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
    const signal = new AbortController().signal
    await searchSkillHub("weather forecast", 20, signal)
    await getHubSkill({ registry: "clawhub", slug: "weather" }, signal)
    await installHubSkill(
      { registry: "clawhub", slug: "weather", version: "1.2" },
      signal,
    )
    assert.equal(
      requests[0].path,
      "/api/skills/hub/search?q=weather+forecast&limit=20&offset=20",
    )
    assert.equal(requests[1].path, "/api/skills/hub/clawhub/weather")
    assert.deepEqual(JSON.parse(requests[2].body), {
      registry: "clawhub",
      slug: "weather",
      version: "1.2",
    })
    assert.ok(requests.every((request) => request.authorization === undefined))
  } finally {
    globalThis.fetch = originalFetch
    await new Promise((resolve) => server.close(resolve))
  }
})
