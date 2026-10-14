import { create } from "zustand"

import type { HubSkillIdentity } from "@/api/skills"

interface SkillHubState {
  query: string
  submittedQuery: string
  selected: HubSkillIdentity | null
}

export const useSkillHubStore = create<SkillHubState>()(() => ({
  query: "",
  submittedQuery: "",
  selected: null,
}))
