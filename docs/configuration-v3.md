# Configuration v3

The saved configuration has `"version": 3` and a `channel_list` map. Each map
key is an instance name. The instance has a platform `type`, common channel
policy, and a `settings` object containing platform-specific options:

```json
{
  "version": 3,
  "channel_list": {
    "telegram": {
      "type": "telegram",
      "enabled": true,
      "allow_from": ["telegram:42"],
      "settings": {
        "token": "example-token",
        "inline": { "enabled": true }
      }
    }
  }
}
```

Common policy keys are `enabled`, `allow_from`, `reasoning_channel_id`,
`group_trigger`, `typing`, and `placeholder`. Put them at the instance level.
Existing Telegram options (including inline, batching, streaming and aliases),
Email, Exa/Parallel search, memory index/embeddings, ASR/Riva, hooks, and
multi-key models retain their existing settings and behavior. String `_comment`
annotations from older examples remain accepted. Unknown settings otherwise
fail with their full field path; syntax/type errors include a line and column.

## Prepare a read-only mount

Unversioned configurations and versions 1 and 2 load and migrate in memory.
Loading does not write, back up, or replace the source. An explicit save writes
v3. Prepare a separate file before mounting it read-only:

```sh
picoclaw migrate --config ./config.json --output ./config.v3.json
```

The output must be a new path. The source and its `.security.yml` are retained.
Preparation reads the source sidecar and resolves model `file://`/`enc://`
credentials, so the new configuration can load without the original sidecar.
With a configured key passphrase, model keys (including arrays) are encrypted
on save. Supply the same passphrase mechanism in the container. Mounted v3
configuration can be read-only; dashboard edits and explicit saves require
writable configuration storage.

## Secrets and environment variables

Legacy `.security.yml` model, channel, web, and skill sections remain supported.
Named instances can put credentials under their own settings:

```yaml
channel_list:
  work:
    settings:
      token: example-work-token
  email:
    settings:
      password: example-email-password
```

Only credential fields belong in the sidecar. Environment variables take
precedence. Existing `PICOCLAW_CHANNELS_TELEGRAM_*` variables configure the
default `telegram` instance. A named `work` Telegram instance uses
`PICOCLAW_CHANNEL_LIST_WORK_TOKEN`, `PICOCLAW_CHANNEL_LIST_WORK_INLINE_ENABLED`,
and the equivalent suffixes for other existing settings. Global Telegram
variables never supply another instance's token. Explicit saves keep an
existing writable sidecar synchronized with edited credentials. Configuration
and sidecar writes are individually atomic; keep both files writable when
editing a configuration that uses a sidecar.

## Conversation identities

Keep the legacy Telegram instance named `telegram` to retain its existing
session keys, inline history, published-inline storage, and archive access.
Renaming it creates a new conversation boundary. New lowercase instance names
isolate direct/group/inline history and archive retrieval, including when an
older `session.dm_scope` shares default direct conversations. Names cannot
contain whitespace, colons or path separators; `cli`, `system`, and `subagent`
are reserved for internal messages. A person's canonical identity remains
`telegram:<user-id>`. Workspace Saved facts retain their existing ownership.

Gateway and channel factories receive an instance-specific typed configuration.
The CLI, TUI, and dashboard save the same v3 format. Existing channel forms use
an adapter for their flat fields; raw dashboard configuration shows the v3
document. Out-of-tree adapters can register their struct prototype with
`config.RegisterChannelSettings(type, Settings{})` and their factory with
`channels.RegisterFactory`; their factory receives only its selected instance
in `cfg.ChannelList` and can call `Channel.Decode` for the typed settings.
