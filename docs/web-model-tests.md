# Model configuration and inference tests

Open **Models**, then edit a model or add one. **Validate configuration** checks
the submitted fields without contacting the provider. **Test model (may incur
usage)** sends one real, short request using the current form values. Neither
action saves changes. Listing models still performs existing local availability
checks; its configured status does not establish that a model can answer.

The inference test supports OpenAI-compatible HTTP models with an API key or an
anonymous/local endpoint, and OpenAI OAuth models bound to a saved Codex account. It uses the selected configuration directly through the
existing provider factory. It does not switch credentials, retry, fall back to
another model, send chat history, execute tools or attach images. CLI and other native provider protocols are outside this test. Codex OAuth tests
refresh only the selected saved connection when needed, and never borrow another
account. A successful test requires a
nonempty answer; provider content and credentials are never included in the
dashboard result.

Each test asks `Reply with OK.` and waits at most 15 seconds, respecting a shorter
configured request timeout. Compatible API-key/local HTTP requests use a
32-token output limit. Codex Responses adapters omit output-limit fields
(the registered ChatGPT route does not support them); their short prompt and
timeout do not guarantee a token or usage cap. Extra-body fields cannot override the selected model, prompt, tools,
streaming or supported token limit. Providers may charge for the request. A model
that returns no text is reported as no answer. Selected OAuth refresh may rotate
that connection's tokens; it never switches to another saved connection.

The editor can retain a blank or masked API key only for the selected saved
entry with the same provider, effective API base, authentication method, account connection and
proxy. Changing that destination requires an explicit replacement key for a
test. Saving a destination edit with a blank key clears the old credential;
masked keys are rejected. An unsaved model never borrows a key by alias or model
identifier. Existing `file://` and `enc://` credentials are resolved by the
configuration loader when testing a saved model. Reference/mask placeholders in
an edit retain only that compatible selected saved key. New references should be
saved before testing, and inline tests accept one key rather than a key list.
A configured multi-key entry tests its first resolved key without rotation.

Cancel aborts the current HTTP request. Closing the form or changing a field
also aborts the request and clears its result. Cancellation cannot undo usage
already accepted by the provider. Errors explain authentication, endpoint,
timeout and unsupported-image failures without exposing response bodies,
credential-bearing URLs or raw provider text.

Authenticated endpoints:

| Endpoint | Action |
| --- | --- |
| `POST /api/models/by-id/{id}/test` | Test the exact saved configured entry by stable ID |
| `POST /api/models/{index}/test` | Compatibility index probe for the configured entry |
| `POST /api/models/test-inline` | Test submitted `ModelConfig` fields, with optional `model_index` for compatible credential retention |
| `POST /api/models/validate` | Validate the same submitted configuration without a network request |

Deterministic checks use temporary configuration stores and fake compatible HTTP
endpoints. Run `go test -tags stdjson ./web/backend/api -run '(Test.*Model.*Inference|TestEditedModel)'`
and `pnpm --dir web/frontend test:models`; neither requires paid credentials.

Registered ChatGPT connections use the public Responses endpoint and preserve
exact model slugs. Missing plan permission blocks inference even when identity
sign-in succeeded. Structured stream failures preserve sanitized quota,
authorization and temporary-server error codes for recovery; failed partial
answers never count as a successful test. See [Codex account connections](codex-accounts.md)
for remote callback instructions and the pending live acceptance checklist.
