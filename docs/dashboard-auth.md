# Dashboard access

The web launcher requires a dashboard password. On first visit, enter the owner's
setup code and set a password
of at least 8 characters (at most 72 UTF-8 bytes), then sign in. Passwords are
used exactly as entered, including spaces. The dashboard access menu in the
header provides **Change dashboard password** and **Sign out**.

On uninitialized startup the launcher writes a random setup code to
`launcher-setup-token`, beside `launcher-auth.db`, readable only by the launcher
owner (`0600` on Unix; a protected owner-only DACL on Windows). Read it on
the server; it is never returned by the public status API or printed in logs.
The automatically opened browser receives it in a URL fragment. For a remote
browser or `-no-browser`, enter it manually. API clients send it in
`X-Picoclaw-Setup-Token` when setting the first password. Missing or incorrect
codes return 403; failure to publish a code disables initial setup with 503.
The code rotates on uninitialized restart and cannot change an existing
password: that requires an authenticated session. Validation failures leave
the code usable for a corrected request.

The launcher stores only a bcrypt hash in `launcher-auth.db`, alongside the
selected application config file. Keep that directory on persistent storage
when running in a container. The database is separate from the application
config, so config export never exports the dashboard password. Startup fails
closed for dashboard access if password storage cannot be opened or read.

Sessions use an HttpOnly, SameSite=Lax cookie, with Secure enabled for HTTPS.
They expire after 31 days and require login again after launcher restart.
Sign out revokes the current session; changing the password revokes every
session and any pending browser bootstrap. Revocation also closes active
WebSocket proxies and other requests attached to that session.

When a password is already configured and browser auto-open is enabled, the
launcher opens a private bootstrap URL. It grants one browser session, expires
after five minutes, and cannot be reused. It is never printed in the launcher
banner or request logs. With `-no-browser`, sign in with the password normally.

Protected access includes configuration, credentials, gateway controls, chat
WebSocket handshakes (`/pico/ws`), and media routes. Unauthenticated API,
WebSocket, and media requests return HTTP 401. Login and setup pages, their
static assets, and authentication endpoints remain available for onboarding.

Login permits ten attempts per client IP in a rolling minute. The launcher uses
the connected client's address; arbitrary `X-Forwarded-For` headers do not
change that address. Auth mutations require JSON and reject foreign
`Origin`/`Referer` and `Sec-Fetch-Site: cross-site` browser requests. Protected
mutations and WebSocket handshakes use the same origin checks.

An HTTPS reverse proxy must preserve the browser-facing Host and set
`X-Forwarded-Proto: https`. The included Vite development proxy preserves Host
so the same origin checks also work during local frontend development.

The existing listen address, explicit `-public` setting, and IP allowlist retain
their behavior. The IP allowlist runs before dashboard authentication and also
applies to public auth endpoints and bootstrap URLs. Dashboard authentication
does not replace the gateway's Pico channel token or its origin policy.

If the password is lost, stop the launcher, back up and remove only
`launcher-auth.db` from the selected config directory, then restart and complete
password setup. Existing application config and conversation history remain in
their own stores.

Focused checks: `go test -tags stdjson ./web/backend/...` and, from
`web/frontend`, `pnpm test:auth`, `pnpm lint`, and `pnpm build:backend`.
