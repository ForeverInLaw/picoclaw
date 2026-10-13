export type LauncherAuthStatus = {
  authenticated: boolean
  initialized: boolean
}

async function authRequest(path: string, body?: object): Promise<Response> {
  const response = await fetch(`/api/auth/${path}`, {
    method: body ? "POST" : "GET",
    headers: body ? { "Content-Type": "application/json" } : undefined,
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
): Promise<void> {
  await authRequest("setup", { password, confirm })
}

export async function postLauncherDashboardLogout(): Promise<void> {
  await authRequest("logout", {})
}
