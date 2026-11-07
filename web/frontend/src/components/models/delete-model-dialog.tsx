import { useMutation } from "@tanstack/react-query"
import { useTranslation } from "react-i18next"

import { type ModelInfo, deleteModel } from "@/api/models"
import { ConfirmDialog } from "@/components/confirm-dialog"

interface DeleteModelDialogProps {
  model: ModelInfo | null
  onClose: () => void
  onDeleted: () => void
}

export function DeleteModelDialog(props: DeleteModelDialogProps) {
  return <DeleteModelForm key={props.model?.id ?? "closed"} {...props} />
}

function DeleteModelForm({
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

  return (
    <ConfirmDialog
      open={model !== null}
      onOpenChange={(open) => !open && onClose()}
      title={t("models.delete.title")}
      description={t("models.delete.description", { name: model?.model_name })}
      confirmLabel={t("models.delete.confirm")}
      pending={deletion.isPending}
      error={deletion.error}
      onConfirm={() => deletion.mutate()}
    />
  )
}
