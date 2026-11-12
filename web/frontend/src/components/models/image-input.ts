// Protocols whose adapters cannot carry image payloads over their transport
// (see SupportsImageInput in pkg/providers). Their image switch stays off.
// Aliases follow CreateProviderFromConfig in pkg/providers/factory_provider.go.
const TEXT_ONLY_PROTOCOLS = new Set([
  "claude-cli",
  "claudecli",
  "codex-cli",
  "codexcli",
  "antigravity",
  "github-copilot",
  "copilot",
])

interface ModelTarget {
  model: string
  authMethod: string
  apiBase: string
}

const protocolOf = (model: string) => {
  const [protocol, rest] = model.trim().split("/", 2)
  return rest === undefined ? "openai" : protocol
}

/** Whether the model's transport can carry images at all. */
export const transportCarriesImages = (model: string) =>
  !TEXT_ONLY_PROTOCOLS.has(protocolOf(model))

/**
 * Initial value of a new model's image switch: Anthropic models and models on
 * OpenAI's own endpoint or OAuth accept images. Saved models keep their flag.
 */
export function defaultAcceptsImages({
  model,
  authMethod,
  apiBase,
}: ModelTarget) {
  switch (protocolOf(model)) {
    case "anthropic":
      return true
    case "openai": {
      const base = apiBase.trim()
      if (authMethod === "oauth" || authMethod === "token" || !base) return true
      return URL.canParse(base) && new URL(base).hostname === "api.openai.com"
    }
    default:
      return false
  }
}
