import {
  queryOptions,
  useMutation,
  useQueryClient,
} from "@tanstack/react-query"

import { getKeyPools } from "@/api/key-pools"
import { modelsQueryOptions } from "@/hooks/use-models"

// Key states change in the gateway at any time, so the listing is polled.
export const keyPoolsQueryOptions = queryOptions({
  queryKey: ["key-pools"],
  queryFn: getKeyPools,
  refetchInterval: 15_000,
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
