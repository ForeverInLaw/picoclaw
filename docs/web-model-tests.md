# Model configuration and inference tests

Open **Models**, then edit a model or add one. **Validate configuration** checks
the submitted fields without contacting the provider. **Test model (may incur
usage)** sends one real, short request using the current form values. Neither
action saves changes. Listing models still performs existing local availability
checks; its configured status does not establish that a model can answer.

The inference test supports OpenAI-compatible HTTP models with an API key or an
anonymous/local endpoint. It uses the selected configuration directly through the
existing provider factory. It does not rotate credentials, retry, fall back to
another model, send chat history, execute tools or attach images. OAuth, CLI and
native provider protocols are outside this test. A successful test requires a
nonempty answer; provider content and credentials are never included in the
dashboard result.

Each test asks `Reply with OK.` with a 32-token output limit and a maximum wait of
15 seconds, respecting a shorter configured request timeout. Extra-body fields
cannot override the selected model, prompt, tools, streaming or token limit.
Providers may charge for the request. A reasoning-heavy model may return no text
within this small limit; that is reported as no answer, rather than success.

The editor can retain a blank or masked API key only for the selected saved
entry with the same provider, effective API base, authentication method and
proxy. Changing that destination requires an explicit replacement key for a
test. Saving a destination edit with a blank key clears the old credential;
masked keys are rejected. An unsaved model never borrows a key by alias or model
identifier. Existing `file://` and `enc://` credentials are resolved by the
configuration loader when testing a saved model. Reference/mask placeholders in
an edit retain only that compatible selected saved key. New references should be
saved before testing, and inline tests accept one key rather than a key list.
Expanded multi-key entries test only their selected key.

Cancel aborts the current HTTP request. Closing the form or changing a field
also aborts the request and clears its result. Cancellation cannot undo usage
already accepted by the provider. Errors explain authentication, endpoint,
timeout and unsupported-image failures without exposing response bodies,
credential-bearing URLs or raw provider text.

Authenticated endpoints:

| Endpoint | Action |
| --- | --- |
| `POST /api/models/{index}/test` | Test the exact saved runtime entry |
| `POST /api/models/test-inline` | Test submitted `ModelConfig` fields, with optional `model_index` for compatible credential retention |
| `POST /api/models/validate` | Validate the same submitted configuration without a network request |

Deterministic checks use temporary configuration stores and fake compatible HTTP
endpoints. Run `go test -tags stdjson ./web/backend/api -run '(Test.*Model.*Inference|TestEditedModel)'`
and `pnpm --dir web/frontend test:models`; neither requires paid credentials.
