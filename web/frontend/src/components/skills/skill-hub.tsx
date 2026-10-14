import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query"
import { useEffect, useRef } from "react"
import { useTranslation } from "react-i18next"
import { toast } from "sonner"

import {
  type HubInstallRequest,
  type SkillSupportItem,
  getHubSkill,
  installHubSkill,
  searchSkillHub,
} from "@/api/skills"
import { getTools } from "@/api/tools"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardContent,
  CardDescription,
  CardFooter,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import { Field, FieldGroup, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetFooter,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet"
import { useSkillHubStore } from "@/store/skill-hub"

export function SkillHub({ installed }: { installed: SkillSupportItem[] }) {
  const { t } = useTranslation()
  const client = useQueryClient()
  const query = useSkillHubStore((state) => state.query)
  const submitted = useSkillHubStore((state) => state.submittedQuery)
  const selected = useSkillHubStore((state) => state.selected)
  const controllerRef = useRef<AbortController | null>(null)
  const policy = useQuery({ queryKey: ["tools"], queryFn: getTools })
  const canSearch =
    policy.data?.tools.find((tool) => tool.name === "find_skills")?.status ===
    "enabled"
  const canInstall =
    policy.data?.tools.find((tool) => tool.name === "install_skill")?.status ===
    "enabled"
  const search = useInfiniteQuery({
    queryKey: ["skill-hub-search", submitted],
    initialPageParam: 0,
    queryFn: ({ pageParam, signal }) =>
      searchSkillHub(submitted, pageParam, signal),
    getNextPageParam: (page) => (page.has_more ? page.next_offset : undefined),
    enabled: canSearch && submitted !== "",
    retry: false,
  })
  const detail = useQuery({
    queryKey: ["skill-hub-detail", selected?.registry, selected?.slug],
    queryFn: ({ signal }) => {
      if (!selected) throw new Error("No selected skill")
      return getHubSkill(selected, signal)
    },
    enabled: selected !== null && canSearch,
    retry: false,
  })
  const install = useMutation({
    mutationFn: ({
      skill,
      controller,
    }: {
      skill: HubInstallRequest
      controller: AbortController
    }) => installHubSkill(skill, controller.signal),
    onSuccess: (result) => {
      toast.success(
        t("pages.agent.skills.hub.installed", { name: result.slug }),
      )
      if (result.is_suspicious)
        toast.warning(t("pages.agent.skills.hub.suspicious"))
    },
    onError: (error, variables) =>
      toast.error(
        variables.controller.signal.aborted
          ? t("pages.agent.skills.hub.canceled")
          : error.message,
      ),
    onSettled: async () => {
      await Promise.all([
        client.invalidateQueries({ queryKey: ["skills"] }),
        client.invalidateQueries({ queryKey: ["skill-hub-search"] }),
      ])
    },
  })

  useEffect(() => () => controllerRef.current?.abort(), [])

  const installSelected = async () => {
    if (
      !selected ||
      controllerRef.current ||
      !detail.data ||
      detail.data.is_malware_blocked
    )
      return
    const controller = new AbortController()
    controllerRef.current = controller
    try {
      await install.mutateAsync({
        skill: {
          ...selected,
          version: detail.data.latest_version || undefined,
        },
        controller,
      })
    } catch {
      /* Mutation reports the error and refreshes the installed list. */
    } finally {
      controllerRef.current = null
    }
  }
  const results = search.data?.pages.flatMap((page) => page.results) || []
  const isInstalled = (slug: string) =>
    installed.some(
      (skill) => skill.source === "workspace" && skill.name === slug,
    ) || results.some((skill) => skill.slug === slug && skill.installed)

  return (
    <section className="flex flex-col gap-4">
      <h2 className="text-lg font-semibold">
        {t("pages.agent.skills.hub.title")}
      </h2>
      <p className="text-muted-foreground text-sm">
        {t("pages.agent.skills.hub.description")}
      </p>
      <form
        className="flex items-end gap-2"
        onSubmit={(event) => {
          event.preventDefault()
          if (!canSearch || !query.trim()) return
          if (query.trim() === submitted) void search.refetch()
          else useSkillHubStore.setState({ submittedQuery: query.trim() })
        }}
      >
        <FieldGroup className="flex-1">
          <Field>
            <FieldLabel htmlFor="skill-hub-search">
              {t("pages.agent.skills.hub.searchLabel")}
            </FieldLabel>
            <Input
              id="skill-hub-search"
              value={query}
              onChange={(event) =>
                useSkillHubStore.setState({ query: event.target.value })
              }
              disabled={!canSearch}
            />
          </Field>
        </FieldGroup>
        <Button type="submit" disabled={!canSearch || !query.trim()}>
          {t("pages.agent.skills.hub.search")}
        </Button>
      </form>
      {policy.error && (
        <p role="alert" className="text-destructive text-sm">
          {policy.error.message}
        </p>
      )}
      {!policy.isPending && !canSearch && (
        <p className="text-muted-foreground text-sm">
          {t("pages.agent.skills.hub.disabled")}
        </p>
      )}
      {search.isLoading && (
        <p role="status" className="text-muted-foreground text-sm">
          {t("labels.loading")}
        </p>
      )}
      {search.error && (
        <p role="alert" className="text-destructive text-sm">
          {search.error.message}
        </p>
      )}
      {submitted && search.isSuccess && results.length === 0 && (
        <p className="text-muted-foreground text-sm">
          {t("pages.agent.skills.hub.empty")}
        </p>
      )}
      <div className="grid gap-3 lg:grid-cols-2">
        {results.map((result) => (
          <Card key={`${result.registry_name}:${result.slug}`} size="sm">
            <CardHeader>
              <CardTitle>{result.display_name || result.slug}</CardTitle>
              <CardDescription>{result.summary}</CardDescription>
            </CardHeader>
            <CardContent className="text-muted-foreground text-sm">
              {result.registry_name} ·{" "}
              {result.version || t("pages.agent.skills.hub.latest")}
            </CardContent>
            <CardFooter className="flex items-center justify-between gap-2">
              {isInstalled(result.slug) ? (
                <Badge variant="secondary">
                  {t("pages.agent.skills.hub.installedLabel")}
                </Badge>
              ) : (
                <span />
              )}
              <Button
                variant="outline"
                size="sm"
                onClick={() =>
                  useSkillHubStore.setState({
                    selected: {
                      registry: result.registry_name,
                      slug: result.slug,
                    },
                  })
                }
              >
                {t("pages.agent.skills.hub.details")}
              </Button>
            </CardFooter>
          </Card>
        ))}
      </div>
      {search.hasNextPage && (
        <Button
          variant="outline"
          className="self-start"
          disabled={search.isFetchingNextPage}
          onClick={() => void search.fetchNextPage()}
        >
          {search.isFetchingNextPage
            ? t("labels.loading")
            : t("pages.agent.skills.hub.more")}
        </Button>
      )}
      <Sheet
        open={selected !== null}
        onOpenChange={(open) => {
          if (!open) useSkillHubStore.setState({ selected: null })
        }}
      >
        <SheetContent>
          <SheetHeader>
            <SheetTitle>
              {detail.data?.display_name || selected?.slug}
            </SheetTitle>
            <SheetDescription>
              {detail.data?.summary ||
                t("pages.agent.skills.hub.detailDescription")}
            </SheetDescription>
          </SheetHeader>
          <div className="flex flex-col gap-3 px-4">
            {detail.isLoading && <p role="status">{t("labels.loading")}</p>}
            {detail.error && (
              <p role="alert" className="text-destructive text-sm">
                {detail.error.message}
              </p>
            )}
            {detail.data && (
              <>
                <p className="text-muted-foreground text-sm">
                  {detail.data.registry_name} ·{" "}
                  {detail.data.latest_version ||
                    t("pages.agent.skills.hub.latest")}
                </p>
                {detail.data.is_malware_blocked && (
                  <p role="alert" className="text-destructive text-sm">
                    {t("pages.agent.skills.hub.blocked")}
                  </p>
                )}
                {detail.data.is_suspicious && (
                  <p className="text-muted-foreground text-sm">
                    {t("pages.agent.skills.hub.suspicious")}
                  </p>
                )}
              </>
            )}
            {!canInstall && (
              <p className="text-muted-foreground text-sm">
                {t("pages.agent.skills.hub.installDisabled")}
              </p>
            )}
            {install.error && (
              <p role="alert" className="text-destructive text-sm">
                {install.variables?.controller.signal.aborted
                  ? t("pages.agent.skills.hub.canceled")
                  : install.error.message}
              </p>
            )}
          </div>
          <SheetFooter>
            <Button
              disabled={
                !canInstall ||
                !detail.data ||
                detail.data.is_malware_blocked ||
                install.isPending ||
                Boolean(selected && isInstalled(selected.slug))
              }
              onClick={() => void installSelected()}
            >
              {selected && isInstalled(selected.slug)
                ? t("pages.agent.skills.hub.installedLabel")
                : install.isPending
                  ? t("pages.agent.skills.hub.installing")
                  : t("pages.agent.skills.hub.install")}
            </Button>
            {install.isPending && (
              <Button
                variant="outline"
                onClick={() => controllerRef.current?.abort()}
              >
                {t("pages.agent.skills.hub.cancel")}
              </Button>
            )}
          </SheetFooter>
        </SheetContent>
      </Sheet>
    </section>
  )
}
