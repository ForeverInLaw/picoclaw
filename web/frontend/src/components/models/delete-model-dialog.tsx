import { IconLoader2 } from "@tabler/icons-react"
import { useMutation } from "@tanstack/react-query"
import { useTranslation } from "react-i18next"

import { type ModelInfo, deleteModel } from "@/api/models"
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog"

interface DeleteModelDialogProps {
  model: ModelInfo | null
  onClose: () => void
  onDeleted: () => void
}

export function DeleteModelDialog({
  model,
  onClose,
  onDeleted,
}: DeleteModelDialogProps) {
  const { t } = useTranslation()
  const deletion = useMutation({
    mutationFn: async () => {
      if (model) await deleteModel(model.id)
    },
    onSuccess: () => {
      onDeleted()
      onClose()
    },
  })
  const deleting = deletion.isPending

  return (
    <AlertDialog open={model !== null} onOpenChange={(v) => !v && onClose()}>
      <AlertDialogContent size="sm">
        <AlertDialogHeader>
          <AlertDialogTitle>{t("models.delete.title")}</AlertDialogTitle>
          <AlertDialogDescription>
            {t("models.delete.description", { name: model?.model_name })}
          </AlertDialogDescription>
        </AlertDialogHeader>
        {deletion.error && (
          <p role="alert" className="text-destructive">
            {deletion.error.message}
          </p>
        )}
        <AlertDialogFooter>
          <AlertDialogCancel onClick={onClose} disabled={deleting}>
            {t("common.cancel")}
          </AlertDialogCancel>
          <AlertDialogAction
            variant="destructive"
            onClick={(event) => {
              event.preventDefault()
              deletion.mutate()
            }}
            disabled={deleting}
          >
            {deleting && <IconLoader2 className="size-4 animate-spin" />}
            {t("models.delete.confirm")}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
