import assert from "node:assert/strict"
import { createServer } from "node:http"
import { test } from "node:test"

import {
  getLauncherAuthStatus,
  postLauncherDashboardLogin,
  postLauncherDashboardLogout,
  postLauncherDashboardSetup,
} from "../src/api/launcher-auth.ts"

test("dashboard setup and login use the cookie API without exposing or trimming passwords", async () => {
  let initialized = false
  let authenticated = false
  const requests = []
  const server = createServer(async (request, response) => {
    let body = ""
    for await (const chunk of request) body += chunk
    requests.push({ path: request.url, method: request.method, body })
    response.setHeader("Content-Type", "application/json")
    if (request.url === "/api/auth/status") {
      response.end(JSON.stringify({ initialized, authenticated }))
      return
    }
    if (request.url === "/api/auth/setup") {
      assert.equal(request.headers["content-type"], "application/json")
      assert.deepEqual(JSON.parse(body), {
        password: " password ",
        confirm: " password ",
      })
      initialized = true
    } else if (request.url === "/api/auth/login") {
      assert.deepEqual(JSON.parse(body), { password: " password " })
      authenticated = true
    } else if (request.url === "/api/auth/logout") {
      authenticated = false
    }
    response.end('{"status":"ok"}')
  })
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve))
  const originalFetch = globalThis.fetch
  globalThis.fetch = (url, options) => {
    assert.equal(options.credentials, "same-origin")
    return originalFetch(
      `http://127.0.0.1:${server.address().port}${url}`,
      options,
    )
  }
  try {
    assert.deepEqual(await getLauncherAuthStatus(), {
      initialized: false,
      authenticated: false,
    })
    await postLauncherDashboardSetup(" password ", " password ")
    await postLauncherDashboardLogin(" password ")
    assert.deepEqual(await getLauncherAuthStatus(), {
      initialized: true,
      authenticated: true,
    })
    await postLauncherDashboardLogout()
    assert.deepEqual(await getLauncherAuthStatus(), {
      initialized: true,
      authenticated: false,
    })
    assert.equal(requests.length, 6)
  } finally {
    globalThis.fetch = originalFetch
    await new Promise((resolve) => server.close(resolve))
  }
})
