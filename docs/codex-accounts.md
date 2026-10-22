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
