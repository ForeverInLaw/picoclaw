import { IconChevronRight } from "@tabler/icons-react"
import { useTranslation } from "react-i18next"

import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/components/ui/collapsible"
import type { ChatActivity } from "@/store/chat"

export function ChatActivityBlocks({
  activities,
}: {
  activities: ChatActivity[]
}) {
  const { t } = useTranslation()
  return (
    <div className="flex flex-col gap-2">
      {activities.map((activity) => (
        <Collapsible
          key={activity.id}
          className="group/activity rounded-lg border"
        >
          <CollapsibleTrigger asChild>
            <Button variant="ghost" className="w-full justify-start">
              <IconChevronRight className="group-data-[state=open]/activity:rotate-90" />
              <span className="min-w-0 flex-1 truncate text-left">
                {activity.kind === "tool"
                  ? activity.tool
                  : t(`chat.activity.${activity.kind}`)}
              </span>
              <Badge
                variant={
                  activity.state === "failed" ? "destructive" : "secondary"
                }
              >
                {t(`chat.activity.${activity.state}`)}
              </Badge>
            </Button>
          </CollapsibleTrigger>
          <CollapsibleContent className="flex flex-col gap-3 px-3 pb-3">
            {activity.arguments &&
              Object.keys(activity.arguments).length > 0 && (
                <div className="flex flex-col gap-1">
                  <span className="text-muted-foreground text-xs">
                    {t("chat.activity.arguments")}
                  </span>
                  <pre className="max-h-48 overflow-auto text-xs break-all whitespace-pre-wrap">
                    {JSON.stringify(activity.arguments, null, 2)}
                  </pre>
                </div>
              )}
            {(activity.error || activity.result) && (
              <pre
                className="max-h-72 overflow-auto text-sm break-words whitespace-pre-wrap"
                role={activity.state === "failed" ? "alert" : undefined}
              >
                {activity.error || activity.result}
              </pre>
            )}
          </CollapsibleContent>
        </Collapsible>
      ))}
    </div>
  )
}
