import { useQuery } from "@tanstack/react-query"

import { type ModelInfo, modelReference } from "@/api/models"

import { modelsQueryOptions, useSetDefaultModel } from "./use-models"

interface UseChatModelsOptions {
  isConnected: boolean
}
function isLocalModel(model: ModelInfo): boolean {
  const local = Boolean(
    model.api_base?.includes("localhost") ||
    model.api_base?.includes("127.0.0.1"),
  )
  return model.auth_method === "local" || (!model.auth_method && local)
}
export function useChatModels({ isConnected }: UseChatModelsOptions) {
  const query = useQuery({
    ...modelsQueryOptions,
    refetchInterval: isConnected ? 10000 : false,
  })
  const changeDefault = useSetDefaultModel()
  const models = query.data?.models ?? []
  const selected = models.find((model) => model.is_default)
  return {
    defaultModelName: selected ? modelReference(selected) : "",
    hasConfiguredModels: models.some((model) => model.configured),
    apiKeyModels: models.filter(
      (model) =>
        model.configured &&
        model.auth_method !== "oauth" &&
        model.auth_method !== "token" &&
        !isLocalModel(model),
    ),
    oauthModels: models.filter(
      (model) =>
        model.configured &&
        (model.auth_method === "oauth" || model.auth_method === "token"),
    ),
    localModels: models.filter(
      (model) => model.configured && isLocalModel(model),
    ),
    handleSetDefault: (reference: string) => changeDefault.mutate(reference),
  }
}
