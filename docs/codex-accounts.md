# Codex account connections

Codex OAuth connections belong to the Picoclaw instance. Open **Credentials** to
see each saved connection's label, stable connection ID, authorization state and
configured models. The ID distinguishes registrations with the same email or
name. **Last successful test** and **Last test** show actual explicit probe
outcomes independently of whether authorization is present.
Open a Codex model in **Models** and select its account before saving. Viewing
these settings never sends an inference request. **Test model** sends one short
request through exactly that model and connection; it does not use a fallback.
Authorization status alone does not establish that a model can answer.

## Existing installations

On the first credential read, an existing `credentials.openai` token set in
`$PICOCLAW_HOME/auth.json` becomes the named connection `codex-default`. Its
initial label uses the saved email when available. The existing compatibility
credential entry remains available to the CLI. Missing credentials do not create
a connected account.

Existing OpenAI models with `auth_method: oauth` or `token` receive the stable
`account_id: codex-default` reference when configuration loads. The reference is
saved on the next configuration write. It identifies the saved connection;
it is separate from OpenAI's workspace/account identifier on the token set.
Models with API keys, their endpoints and the primary model remain unchanged.
Login saves a selectable Codex model only when the compatibility connection has
none; it never promotes it to primary. Logout retains the model's connection
reference so reauthorization can restore it.

Credentials, issued OAuth client configuration and registration identity remain
in the protected auth store. Listing connections returns a separate sanitized
view. Atomic writes use mode `0600`; store transactions and rotating refreshes
use filesystem locks shared by the launcher and gateway. Refreshing one account
preserves concurrent account labels and other sessions. Disabling during refresh
retains newly rotated credentials for the same registration, while blocking
inference until re-enabled. Deletion, logout or a replaced authorization prevents
a late refresh from saving over the changed session.

Keep the auth directory private and retain the `auth.json*.lock` files while
Picoclaw processes run. They provide coordination between processes; deleting a
lock file while it is held can split ownership. Back up both configuration and
`auth.json` before downgrading. Older versions do not understand named account
references and can access only the compatibility connection.

The protected `GET /api/oauth/accounts` endpoint returns connection labels,
identity hints, enabled/authorization state and model references. No access,
refresh or ID tokens, client secrets, callback codes or verifiers are returned.

## Add an account from a remote dashboard

1. Open **Credentials** and choose **Continue with ChatGPT**. OpenAI sign-in opens
   in another browser tab; the dashboard keeps the pending attempt available.
2. Complete account/workspace selection and authorize Picoclaw. The final address
   starts with `http://127.0.0.1:1455/auth/callback`. The browser may report that
   this local page cannot connect. Copy the **complete address**, including its
   query parameters, from the address bar anyway.
3. Paste the address into **Complete callback URL** in Picoclaw and choose
   **Connect account**. Closing the sheet clears its input; **Resume sign-in**
   reopens the pending attempt until it completes or expires after ten minutes.
4. Open **Models**, add an `openai/<model slug>` model, and select the saved
   ChatGPT account instead of an API key. Save it, then explicitly use **Test
   model**. Connecting an account never changes the primary model.

Treat the callback address as sensitive and do not paste it into logs, tickets,
chat messages or browser storage. The dashboard sends it once to the server and
clears the input. Pending attempts belong to the dashboard session that started
sign-in. They do not survive a server restart; start a fresh sign-in if needed.

New connections use OpenAI's public registration contract: dynamic first
registration, a separately retained issued client ID, signed OIDC identity
validation, and the public Responses API. Email alone never merges connections.
Returning authorization must match the saved issuer, subject and issued client.
When the token grant lacks ChatGPT plan permission, the identity is retained and
shown as requiring plan permission; no inference request is sent through it.

The migrated `codex-default` connection keeps the existing CLI authorization and
endpoint. Its old credential record has no verified dynamic-registration subject
or retained ID token, so the dashboard cannot safely reconnect it by email or
workspace ID. Renew it with its existing CLI flow, or add a separately verified
browser connection and explicitly rebind models. Existing working credentials
are preserved during this transition.

The new HTTP Responses route sends the selected model slug unchanged, uses
`store: false` and streaming, groups local functions in a namespace, and retains
completed tool calls/results in normal conversation history. Image input remains
unsupported by this adapter until its existing Media boundary can serialize it.

Some streams deliver completed messages and tool calls in `response.output_item.done`
events while the final response snapshot has an empty `output`. Picoclaw retains
those completed items in output order and uses them only after a successful
`response.completed` when that final list is empty. Partial deltas, failed and
incomplete responses never become a completed answer through this recovery.

Token refresh, refresh-lock waits and the final account save respect request
cancellation. Confirmed unusable refresh tokens clear only that account's token
set; its registration mapping remains available for reauthorization. Temporary
provider errors preserve credentials and retain sanitized recovery metadata.

See OpenAI's [registration and sign-in contract](https://developers.openai.com/siwc/token-sharing-open-source/sign-in),
[session guidance](https://developers.openai.com/siwc/token-sharing-open-source/profiles-and-sessions)
and [preview limitations](https://developers.openai.com/siwc/token-sharing-open-source/preview-limitations).

## Live acceptance checklist

Automated tests use fake OAuth, signed ID tokens/JWKS and inference endpoints.
They do **not** establish that OpenAI currently accepts manual forwarding of the
loopback callback. This browser check is pending until an administrator with an
eligible account performs it; CI requires no subscription or personal token.

1. Run the dashboard on a remote instance, start **Continue with ChatGPT**, and
   complete real OpenAI account/workspace consent. Record only whether OpenAI
   accepted the authorization parameters; keep all credentials private.
2. Copy the final loopback address even if the local page fails to connect, then
   complete it in the initiating dashboard session. Confirm one named account
   appears and the existing primary/API-key models retain their settings.
3. Add a model available to that account, explicitly select its connection, and
   run **Test model**. Confirm the chosen model answers; this request can consume
   the account's inference allowance.
4. Make a short ordinary assistant request with a harmless local tool and confirm
   its result continues into the final answer. Check only sanitized outcomes.
5. After token expiry is near, explicitly test again and confirm refresh uses the
   same registration. Reauthorize that account and confirm its saved reference
   remains stable. A second account with the same email must remain distinct.
6. Report pass/fail for callback completion, selected-model inference and refresh,
   with any sanitized status/code. Never share callback URLs, codes or tokens.

Signing out clears the selected credentials (or all credentials for explicit
CLI logout-all), while retaining the stable host identity and saved account/client
mappings. Signing out and deleting an account connection are separate actions.

## Manage an individual connection

**Credentials** provides rename, reconnect, enable/disable, delete and **Test
account** actions for each saved connection. Enter the exact OpenAI model slug
for a test; it sends one short request through that connection only. Both this
action and the saved-model test record only timestamp, model, status and latency,
with the last successful timestamp retained after a later failed test. Provider
answers and raw errors are not saved. Opening settings never probes inference.

Renaming preserves model references. Reconnect uses the selected registration's
issued client and verified issuer/subject, retaining its current name and enabled
state. The legacy connection follows the migration guidance above. Disabling
blocks the next outbound call, including the next inference step inside an
already running task; an already sent call can finish. Re-enabling retains the
saved authorization and previous test history.

Delete returns the names of configured models that still reference the account;
replace their account selection in **Models** first. Disabling remains available
while referenced. Deletion and model binding saves share a cross-process writer
guard, so a concurrent model edit cannot leave a reference to a deleted account.
A late refresh, reconnect or test result cannot recreate a deleted connection.

| Protected endpoint | Action |
| --- | --- |
| `PATCH /api/oauth/accounts/{id}` | Update only supplied `label` and/or `enabled` fields atomically |
| `POST /api/oauth/accounts/{id}/test` | Probe exact connection with `{ "model": "<OpenAI slug>" }` |
| `DELETE /api/oauth/accounts/{id}` | Remove unused connection; `409` lists blocking model names |

Tests use authenticated dashboard sessions, temporary stores and fake
OAuth/inference endpoints. Run `go test ./pkg/auth ./web/backend/api` and
`pnpm --dir web/frontend test:models`; no personal tokens or paid calls are needed.

Token failures use their OAuth machine codes. A rejected client configuration (`invalid_client`) suspends further calls through every alias of that registration and permits an independent configured reserve without deleting the saved tokens; repair the client configuration. Runtime reports `client_configuration` separately from revoked authorization. Saving a corrected registration clears that session-scoped suspension. Temporary service codes preserve tokens and use service recovery even if the token endpoint returns HTTP400. Malformed-request codes remain terminal.

Legacy CLI-compatible accounts retain their existing model substitution policy. Runtime successful outcomes show the model actually sent on the wire, alongside the unchanged configured-model ID and account; captured routing chains keep the configured model. Exact explicit probes continue to use the selected slug without substitution.

## Native Codex image generation

The optional `generate_image` tool generates one image or edits up to five
PNG/JPEG/WebP references with `gpt-image-2`. It uses Codex's native
`https://chatgpt.com/backend-api/codex/images/generations` and `images/edits`
routes, independently of the primary text model and image-understanding routing.
Requests consume the selected account's image allowance. Configuration and login
never generate images.

A native Codex connection is required. Public **Continue with ChatGPT**
registrations use `api.openai.com/v1`; those tokens are not interchangeable with
native Codex authorization. On a headless server, connect the native account:

```sh
picoclaw auth login --provider openai --device-code
```

Complete the displayed device sign-in. The existing CLI flow saves the separate
`codex-default` connection without replacing public registrations or the primary
model. Bind that connection explicitly in `config.json`, then restart the gateway:

```json
{
  "tools": {
    "generate_image": {
      "enabled": true,
      "account_id": "codex-default"
    }
  }
}
```

The tool is disabled by default. It never selects another account or retries an
image request automatically. Disabled, removed or expired connections use the
existing account authorization and refresh rules. An image-specific rejection
does not revoke a working text connection.

Ask for an image in a regular chat. For edits, attach an image and describe the
changes; retained `attachment://` references remain scoped to their conversation.
Allowed workspace paths can also be used, with the same read boundaries as
`read_file`. Reference images total at most 32 MiB; output is bounded by both
32 MiB and the configured media limit. Files are saved privately under unique
`generated-image-*` paths in the agent workspace and queued through normal media
delivery. Keep or remove these workspace files as needed. Inline and no-history
requests cannot invoke the tool.

The native wire format follows OpenAI Codex revision
[`5a1c247`](https://github.com/openai/codex/tree/5a1c247dd5fd682c22a34af93df697dd06300a32/codex-rs/ext/image-generation).
This backend contract can change independently of the public Images API.
