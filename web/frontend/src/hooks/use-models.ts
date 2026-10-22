import {
  queryOptions,
  useMutation,
  useQueryClient,
} from "@tanstack/react-query"

import { getModelRouting } from "@/api/model-routing"
import { getModels, setDefaultModel } from "@/api/models"

export const modelsQueryOptions = queryOptions({
  queryKey: ["models"],
  queryFn: getModels,
})
export const modelRoutingQueryOptions = queryOptions({
  queryKey: ["model-routing"],
  queryFn: getModelRouting,
})

export function useInvalidateModels() {
  const client = useQueryClient()
  return () =>
    Promise.all([
      client.invalidateQueries({ queryKey: modelsQueryOptions.queryKey }),
      client.invalidateQueries({ queryKey: modelRoutingQueryOptions.queryKey }),
      client.invalidateQueries({ queryKey: ["codex-accounts"] }),
    ])
}

export function useSetDefaultModel() {
  const invalidate = useInvalidateModels()
  return useMutation({ mutationFn: setDefaultModel, onSuccess: invalidate })
}
