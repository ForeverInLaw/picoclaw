import assert from "node:assert/strict"
import { once } from "node:events"
import { createServer } from "node:http"
import { test } from "node:test"

test("manual callback completion is explicit and preserves the selected flow", async () => {
  const { completeCodexOAuth } = await import("../src/api/oauth.ts")
  const { getCodexAccounts } = await import("../src/api/codex-accounts.ts")
  const requests = []
  const server = createServer(async (request, response) => {
    let body = ""
    for await (const part of request) body += part
    requests.push({ path: request.url, method: request.method, body })
    response.writeHead(200, { "Content-Type": "application/json" }).end(
      JSON.stringify(
        request.method === "GET"
          ? { accounts: [] }
          : {
              flow_id: "selected-flow",
              provider: "openai",
              method: "browser",
              status: "success",
              account_id: "saved-account",
            },
      ),
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
    await getCodexAccounts()
    assert.deepEqual(requests, [
      { path: "/api/oauth/accounts", method: "GET", body: "" },
    ])
    const callback =
      "http://127.0.0.1:1455/auth/callback?code=fixture-code&state=fixture-state&client_id=oaiapp_fixture"
    assert.equal(
      (await completeCodexOAuth("selected-flow", callback)).account_id,
      "saved-account",
    )
    assert.deepEqual(requests[1], {
      path: "/api/oauth/flows/selected-flow/complete",
      method: "POST",
      body: JSON.stringify({ callback_url: callback }),
    })
    assert.equal(requests.length, 2)
  } finally {
    globalThis.fetch = originalFetch
    await new Promise((resolve) => server.close(resolve))
  }
})

test("account management keeps the exact account and selected model", async () => {
  const { updateCodexAccount, deleteCodexAccount, testCodexAccount } =
    await import("../src/api/codex-accounts.ts")
  const originalFetch = globalThis.fetch
  const requests = []
  globalThis.fetch = async (path, options) => {
    requests.push({ path, ...options })
    return Response.json({ status: "success" })
  }
  try {
    await updateCodexAccount("second", { label: "Work" })
    await updateCodexAccount("second", { enabled: false })
    await testCodexAccount("second", "gpt-6.1-sol")
    await deleteCodexAccount("second")
    assert.deepEqual(
      requests.map(({ path, method, body }) => ({ path, method, body })),
      [
        {
          path: "/api/oauth/accounts/second",
          method: "PATCH",
          body: '{"label":"Work"}',
        },
        {
          path: "/api/oauth/accounts/second",
          method: "PATCH",
          body: '{"enabled":false}',
        },
        {
          path: "/api/oauth/accounts/second/test",
          method: "POST",
          body: '{"model":"gpt-6.1-sol"}',
        },
        {
          path: "/api/oauth/accounts/second",
          method: "DELETE",
          body: undefined,
        },
      ],
    )
    globalThis.fetch = async () =>
      Response.json(
        {
          message:
            "Replace this account in the listed models before deleting it.",
          models: ["Work model", "Backup model"],
        },
        { status: 409 },
      )
    await assert.rejects(
      deleteCodexAccount("second"),
      /Work model, Backup model/,
    )
  } finally {
    globalThis.fetch = originalFetch
  }
})
