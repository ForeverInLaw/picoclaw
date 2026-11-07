import {
  queryOptions,
  useMutation,
  useQueryClient,
} from "@tanstack/react-query"

import { getKeyPools, getPoolModels } from "@/api/key-pools"
import { modelsQueryOptions } from "@/hooks/use-models"

// Key states change in the gateway at any time, so the listing is polled.
export const keyPoolsQueryOptions = queryOptions({
  queryKey: ["key-pools"],
  queryFn: getKeyPools,
  refetchInterval: 15_000,
})

// Anthropic's model list for a pool. Its key sits under the pools' key, so a
// Key pool edit refreshes it too.
export const poolModelsQueryOptions = (poolID: string) =>
  queryOptions({
    queryKey: [...keyPoolsQueryOptions.queryKey, poolID, "models"],
    queryFn: () => getPoolModels(poolID),
    staleTime: 5 * 60_000,
    retry: false,
  })

// A Key pool edit refreshes the pools and the models whose availability
// depends on their pool's enabled keys.
export function useKeyPoolMutation<TVariables = void>(
  mutationFn: (variables: TVariables) => Promise<unknown>,
) {
  const client = useQueryClient()
  return useMutation({
    mutationFn,
    onSuccess: () =>
      Promise.all([
        client.invalidateQueries({ queryKey: keyPoolsQueryOptions.queryKey }),
        client.invalidateQueries({ queryKey: modelsQueryOptions.queryKey }),
      ]),
  })
}
