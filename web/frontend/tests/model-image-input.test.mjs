import assert from "node:assert/strict"
import { test } from "node:test"

test("new models default the image switch from their protocol and endpoint", async () => {
  const { defaultAcceptsImages, transportCarriesImages } =
    await import("../src/components/models/image-input.ts")
  const target = (model, apiBase = "", authMethod = "") => ({
    model,
    apiBase,
    authMethod,
  })

  assert.equal(defaultAcceptsImages(target("anthropic/claude-haiku-5-5")), true)
  assert.equal(defaultAcceptsImages(target("openai/gpt-5")), true)
  assert.equal(defaultAcceptsImages(target("gpt-6-luna", "", "oauth")), true)
  assert.equal(
    defaultAcceptsImages(
      target("openai/llama", "https://inference.example/v1"),
    ),
    false,
  )
  assert.equal(defaultAcceptsImages(target("groq/llama-3.3-70b")), false)

  assert.equal(transportCarriesImages("claude-cli/claude-code"), false)
  assert.equal(transportCarriesImages("openai/zai-org/GLM-5.3-Flash"), true)
})
