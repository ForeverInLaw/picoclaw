import { IconKey, IconLoader2 } from "@tabler/icons-react"
import { useQuery } from "@tanstack/react-query"
import { useState } from "react"
import { useTranslation } from "react-i18next"

import {
  type KeyPool,
  addPoolKey,
  createKeyPool,
  deleteKeyPool,
  renameKeyPool,
} from "@/api/key-pools"
import { ConfirmDialog } from "@/components/confirm-dialog"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { keyPoolsQueryOptions, useKeyPoolMutation } from "@/hooks/use-key-pools"

import { EditableName } from "./editable-name"
import { KeyPoolKeyRow } from "./key-pool-key-row"

function AddKeyForm({ poolID }: { poolID: string }) {
  const { t } = useTranslation()
  const [label, setLabel] = useState("")
  const [apiKey, setAPIKey] = useState("")
  const add = useKeyPoolMutation(() =>
    addPoolKey(poolID, label.trim(), apiKey.trim()),
  )
  return (
    <form
      className="flex flex-col gap-2"
      onSubmit={(event) => {
        event.preventDefault()
        add.mutate(undefined, {
          onSuccess: () => {
            setLabel("")
            setAPIKey("")
          },
        })
      }}
    >
      <div className="flex flex-wrap gap-2">
        <Input
          className="sm:max-w-48"
          aria-label={t("credentials.keyPools.keyLabel")}
          placeholder={t("credentials.keyPools.keyLabel")}
          value={label}
          onChange={(event) => setLabel(event.target.value)}
          required
          maxLength={200}
          disabled={add.isPending}
        />
        <Input
          className="min-w-48 flex-1"
          type="password"
          autoComplete="off"
          aria-label={t("credentials.keyPools.apiKey")}
          placeholder={t("credentials.keyPools.apiKey")}
          value={apiKey}
          onChange={(event) => setAPIKey(event.target.value)}
          required
          disabled={add.isPending}
        />
        <Button
          type="submit"
          disabled={add.isPending || !label.trim() || !apiKey.trim()}
        >
          {add.isPending ? (
            <IconLoader2 className="size-4 animate-spin" />
          ) : (
            <IconKey className="size-4" />
          )}
          {t(
            add.isPending
              ? "credentials.keyPools.checkingKey"
              : "credentials.keyPools.addKey",
          )}
        </Button>
      </div>
      {add.error && (
        <p role="alert" className="text-destructive text-sm">
          {add.error.message}
        </p>
      )}
    </form>
  )
}

function KeyPoolCard({ pool }: { pool: KeyPool }) {
  const { t } = useTranslation()
  const [deleteOpen, setDeleteOpen] = useState(false)
  const rename = useKeyPoolMutation((name: string) =>
    renameKeyPool(pool.id, name),
  )
  const remove = useKeyPoolMutation(() => deleteKeyPool(pool.id))

  return (
    <section className="bg-card flex flex-col gap-4 rounded-xl border p-4">
      <div className="flex flex-col gap-2">
        <EditableName
          label={t("credentials.keyPools.poolName")}
          value={pool.name}
          disabled={rename.isPending || remove.isPending}
          onSave={(name) => rename.mutateAsync(name)}
        >
          <h3 className="text-base font-semibold">{pool.name}</h3>
        </EditableName>
        <p className="text-muted-foreground text-sm">
          {t("credentials.keyPools.models")}: {pool.models.join(", ") || "—"}
        </p>
        {rename.error && (
          <p role="alert" className="text-destructive text-sm">
            {rename.error.message}
          </p>
        )}
      </div>
      {pool.keys.length > 0 && (
        <ul className="flex flex-col gap-3">
          {pool.keys.map((key) => (
            <KeyPoolKeyRow key={key.id} poolID={pool.id} poolKey={key} />
          ))}
        </ul>
      )}
      <AddKeyForm poolID={pool.id} />
      <div>
        <Button
          variant="ghost"
          size="sm"
          className="text-destructive hover:bg-destructive/10 hover:text-destructive"
          disabled={remove.isPending}
          onClick={() => {
            remove.reset()
            setDeleteOpen(true)
          }}
        >
          {t("credentials.keyPools.deletePool")}
        </Button>
      </div>
      <ConfirmDialog
        open={deleteOpen}
        onOpenChange={setDeleteOpen}
        title={t("credentials.keyPools.deletePoolTitle", { name: pool.name })}
        description={t("credentials.keyPools.deletePoolDescription")}
        confirmLabel={t("credentials.keyPools.deletePool")}
        pending={remove.isPending}
        error={remove.error}
        onConfirm={() => remove.mutate()}
      />
    </section>
  )
}

function CreatePoolForm() {
  const { t } = useTranslation()
  const [name, setName] = useState("")
  const create = useKeyPoolMutation(() => createKeyPool(name.trim()))
  return (
    <form
      className="flex flex-col gap-2"
      onSubmit={(event) => {
        event.preventDefault()
        create.mutate(undefined, { onSuccess: () => setName("") })
      }}
    >
      <div className="flex gap-2 sm:max-w-md">
        <Input
          aria-label={t("credentials.keyPools.poolName")}
          placeholder={t("credentials.keyPools.poolName")}
          value={name}
          onChange={(event) => setName(event.target.value)}
          required
          maxLength={200}
          disabled={create.isPending}
        />
        <Button
          type="submit"
          variant="outline"
          disabled={create.isPending || !name.trim()}
        >
          {t("credentials.keyPools.createPool")}
        </Button>
      </div>
      {create.error && (
        <p role="alert" className="text-destructive text-sm">
          {create.error.message}
        </p>
      )}
    </form>
  )
}

// Anthropic Key pools: named sets of API keys, one per organization, that
// configured models reference instead of a single key.
export function KeyPools() {
  const { t } = useTranslation()
  const listing = useQuery(keyPoolsQueryOptions)

  return (
    <section className="flex flex-col gap-4 pb-6">
      <div>
        <h2 className="text-base font-semibold">
          {t("credentials.keyPools.title")}
        </h2>
        <p className="text-muted-foreground mt-1 text-sm">
          {t("credentials.keyPools.description")}
        </p>
      </div>
      {listing.isPending ? (
        <p className="text-muted-foreground text-sm">
          {t("credentials.keyPools.loading")}
        </p>
      ) : listing.error ? (
        <p role="alert" className="text-destructive text-sm">
          {listing.error.message}
        </p>
      ) : (
        listing.data.pools.map((pool) => (
          <KeyPoolCard key={pool.id} pool={pool} />
        ))
      )}
      <CreatePoolForm />
    </section>
  )
}
