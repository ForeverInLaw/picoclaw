export type LauncherAuthStatus = {
  authenticated: boolean
  initialized: boolean
}

async function authRequest(
  path: string,
  body?: object,
  setupToken?: string,
): Promise<Response> {
  const headers = body
    ? new Headers({ "Content-Type": "application/json" })
    : undefined
  if (setupToken) headers?.set("X-Picoclaw-Setup-Token", setupToken)
  const response = await fetch(`/api/auth/${path}`, {
    method: body ? "POST" : "GET",
    headers,
    credentials: "same-origin",
    body: body ? JSON.stringify(body) : undefined,
  })
  if (!response.ok) {
    let message = `Request failed with status ${response.status}`
    try {
      const error: unknown = await response.json()
      if (
        typeof error === "object" &&
        error !== null &&
        "error" in error &&
        typeof error.error === "string"
      ) {
        message = error.error
      }
    } catch {
      // A proxy may return a non-JSON error.
    }
    throw new Error(message)
  }
  return response
}

export async function getLauncherAuthStatus(): Promise<LauncherAuthStatus> {
  const response = await authRequest("status")
  const status: unknown = await response.json()
  if (
    typeof status !== "object" ||
    status === null ||
    !("authenticated" in status) ||
    !("initialized" in status) ||
    typeof status.authenticated !== "boolean" ||
    typeof status.initialized !== "boolean"
  ) {
    throw new Error("Invalid dashboard authentication status")
  }
  return {
    authenticated: status.authenticated,
    initialized: status.initialized,
  }
}

export async function postLauncherDashboardLogin(
  password: string,
): Promise<void> {
  await authRequest("login", { password })
}

export async function postLauncherDashboardSetup(
  password: string,
  confirm: string,
  setupToken?: string,
): Promise<void> {
  await authRequest("setup", { password, confirm }, setupToken)
}

export async function postLauncherDashboardLogout(): Promise<void> {
  await authRequest("logout", {})
}
