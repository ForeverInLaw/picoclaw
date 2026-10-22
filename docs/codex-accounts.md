# Codex account connections

Codex OAuth connections belong to the Picoclaw instance. Open **Credentials** to
see each saved connection's label, authorization state and configured models.
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
preserves concurrent account labels and other sessions. Deletion or disablement
during refresh prevents the late token set from being used or saved.

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
