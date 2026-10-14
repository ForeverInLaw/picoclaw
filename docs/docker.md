# 🐳 Docker & Quick Start Guide

> Back to [README](../README.md)

## 🐳 Docker Compose

Build the gateway, full MCP runtime, and web launcher from the checked-out source:

```bash
make docker-build
make docker-build-full
make docker-build-launcher
```

The local tags are `picoclaw:local`, `picoclaw:full-local`, and
`picoclaw:launcher-local`. Builds reuse `make build` and `make build-launcher`;
the launcher embeds the UI using the repository's locked pnpm version and
frozen lockfile. The existing ASR/Riva Go build path remains enabled. Full MCP
adds Node/npm, Python and distribution-packaged uv/uvx. All three runtimes
include Bubblewrap and run as UID/GID 1000.

The state directory is `/data` (`PICOCLAW_HOME`), including `config.json`,
`launcher-auth.db`, and `workspace/`. For the basic Compose file, prepare its
bind mount and onboard once:

```bash
mkdir -p docker/data
# On Linux, make the bind directory writable by the image's UID/GID.
sudo chown 1000:1000 docker/data
docker compose -f docker/docker-compose.yml run --rm --entrypoint picoclaw picoclaw-agent onboard
```

Set provider credentials and channels in `docker/data/config.json`, then start:

```bash
docker compose -f docker/docker-compose.yml --profile gateway up -d
```

> [!TIP]
> **Docker Users**: By default, the Gateway listens on `127.0.0.1` which is not accessible from the host. If you need to access the health endpoints or expose ports, set `PICOCLAW_GATEWAY_HOST=0.0.0.0` in your environment or update `config.json`.

```bash
docker compose -f docker/docker-compose.yml logs -f picoclaw-gateway

docker compose -f docker/docker-compose.yml --profile gateway down
```

### Launcher Mode (Web Console)

The source launcher image includes `picoclaw` and the web launcher. The release
launcher image also includes the existing TUI binary. Both start the web
console and manage their owned gateway process automatically.

```bash
docker compose -f docker/docker-compose.yml --profile launcher up -d
```

Read the owner's setup code from the running container:

```bash
docker compose -f docker/docker-compose.yml --profile launcher exec picoclaw-launcher cat /data/launcher-setup-token
```

Open http://localhost:18800, enter that code and create the dashboard password. The Compose
ports bind to host loopback. Authentication setup persists in
`docker/data/launcher-auth.db`; browser sessions expire on restart and require
login again. Keep the whole state directory writable and persistent. See
[dashboard authentication](dashboard-auth.md).

### Prepared read-only configuration

Prepare a v3 configuration before mounting it; see
[configuration migration](configuration-v3.md). Bind the **file** read-only
inside a writable state directory. Mounting the whole parent read-only prevents
creation of the authentication database.

For a launcher, override its volumes with this layout:

```yaml
services:
  picoclaw-launcher:
    volumes:
      - ./data:/data
      - ./prepared-config.json:/data/config.json:ro
```

Paths are relative to the first Compose file's directory (`docker/` in the
examples). Ensure the prepared file exists. Configuration reads, password
setup, login, restart and workspace writes work with this layout. Dashboard
configuration edits fail explicitly while the file is read-only.

`docker-compose.full.yml` already combines a writable named state/workspace
volume with `config/config.json` mounted read-only. Set the prepared config's
workspace to `/data/workspace`; onboard or copy the workspace templates into
that volume before relying on its custom instructions. Changing to UID 1000
requires adjusting ownership of state/workspace volumes from older root-run
images.

### Optional Linux isolation

Isolation remains disabled by default. Enable `isolation.enabled` in the
configuration, then opt into the runtime overlay on a host that passes the
acceptance check:

```bash
docker compose -f docker/docker-compose.yml -f docker/docker-compose.isolation.yml --profile launcher up -d
# Full MCP agent/gateway: the full file overrides the basic services.
docker compose -f docker/docker-compose.yml -f docker/docker-compose.full.yml -f docker/docker-compose.isolation.yml --profile gateway up -d
```

The verified mode keeps UID 1000, drops all outer capabilities, enables
no-new-privileges, and uses a vendored Moby seccomp profile with five explicit
syscall grants for Bubblewrap. It also explicitly relaxes outer AppArmor and
system-path masks so the child can mount its own `/proc`. It requires neither
privileged mode nor `SYS_ADMIN`. These policy changes are opt-in; review
[the profile and its provenance](../docker/ISOLATION.md). A host-specific
AppArmor profile permitting the same namespace/mount contract may replace the
unconfined option.

Default Docker restrictions on the verified host deny enabled isolation with
a Bubblewrap capability error. PicoClaw never retries outside the sandbox.
Kernel, Docker daemon and LSM policies differ across hosts; installing the
binary alone does not prove isolation can run. See [isolation](isolation.md)
for explicit mounts, filtered environment, networking and cancellation.

### Container acceptance

On Linux with Docker Compose, Go and Python 3, build the three local tags above,
then run:

```bash
make docker-test-packaging
# Or select existing image tags explicitly:
python3 scripts/test-docker-packaging.py --launcher picoclaw:launcher-local --gateway picoclaw:local --full picoclaw:full-local
```

The script uses temporary state, a local fake HTTP provider and an existing
fake stdio MCP server. It checks embedded UI, setup/login, persistent auth,
read-only config, gateway startup/restart/shutdown, fresh workspace templates,
Compose profile resolution, actual Shell/MCP mount/env/private-proc/network
behavior, default denied diagnostics, and ordinary unisolated MCP. It prints
image content IDs, revision labels and the acceptance checkout. Containers
and test networks are removed; images remain for inspection. No live provider
or Telegram credentials are needed.

### Agent Mode (One-shot)

```bash
# Ask a question
docker compose -f docker/docker-compose.yml run --rm picoclaw-agent -m "What is 2+2?"

# Interactive mode
docker compose -f docker/docker-compose.yml run --rm picoclaw-agent
```

### Update

```bash
make docker-build docker-build-launcher
docker compose -f docker/docker-compose.yml --profile gateway up -d
```

### 🚀 Quick Start

> [!TIP]
> Set your API Key in `~/.picoclaw/config.json`. Get API Keys: [Volcengine (CodingPlan)](https://www.volcengine.com/activity/codingplan?utm_campaign=PicoClaw&utm_content=PicoClaw&utm_medium=devrel&utm_source=OWO&utm_term=PicoClaw) (LLM) · [OpenRouter](https://openrouter.ai/keys) (LLM) · [Zhipu](https://open.bigmodel.cn/usercenter/proj-mgmt/apikeys) (LLM). Web search is optional — get a free [Tavily API](https://tavily.com) (1000 free queries/month) or [Brave Search API](https://brave.com/search/api) (2000 free queries/month).

**1. Initialize**

```bash
picoclaw onboard
```

**2. Configure** (`~/.picoclaw/config.json`)

```json
{
  "agents": {
    "defaults": {
      "workspace": "~/.picoclaw/workspace",
      "model_name": "gpt-5.4",
      "max_tokens": 8192,
      "temperature": 0.7,
      "max_tool_iterations": 20
    }
  },
  "model_list": [
    {
      "model_name": "ark-code-latest",
      "model": "volcengine/ark-code-latest",
      "api_key": "sk-your-api-key",
      "api_base":"https://ark.cn-beijing.volces.com/api/coding/v3"
    },
    {
      "model_name": "gpt-5.4",
      "model": "openai/gpt-5.4",
      "api_key": "your-api-key",
      "request_timeout": 300
    },
    {
      "model_name": "claude-sonnet-4.6",
      "model": "anthropic/claude-sonnet-4.6",
      "api_key": "your-anthropic-key"
    }
  ],
  "tools": {
    "web": {
      "enabled": true,
      "fetch_limit_bytes": 10485760,
      "format": "plaintext",
      "brave": {
        "enabled": false,
        "api_key": "YOUR_BRAVE_API_KEY",
        "max_results": 5
      },
      "tavily": {
        "enabled": false,
        "api_key": "YOUR_TAVILY_API_KEY",
        "max_results": 5
      },
      "duckduckgo": {
        "enabled": true,
        "max_results": 5
      },
      "perplexity": {
        "enabled": false,
        "api_key": "YOUR_PERPLEXITY_API_KEY",
        "max_results": 5
      },
      "searxng": {
        "enabled": false,
        "base_url": "http://your-searxng-instance:8888",
        "max_results": 5
      }
    }
  }
}
```

> **New**: The `model_list` configuration format allows zero-code provider addition. See [Model Configuration](#model-configuration-model_list) for details.
> `request_timeout` is optional and uses seconds. If omitted or set to `<= 0`, PicoClaw uses the default timeout (120s).

**3. Get API Keys**

* **LLM Provider**: [OpenRouter](https://openrouter.ai/keys) · [Zhipu](https://open.bigmodel.cn/usercenter/proj-mgmt/apikeys) · [Anthropic](https://console.anthropic.com) · [OpenAI](https://platform.openai.com) · [Gemini](https://aistudio.google.com/api-keys)
* **Web Search** (optional):
  * [Brave Search](https://brave.com/search/api) - Paid ($5/1000 queries, ~$5-6/month)
  * [Perplexity](https://www.perplexity.ai) - AI-powered search with chat interface
  * [SearXNG](https://github.com/searxng/searxng) - Self-hosted metasearch engine (free, no API key needed)
  * [Tavily](https://tavily.com) - Optimized for AI Agents (1000 requests/month)
  * DuckDuckGo - Built-in fallback (no API key required)

> **Note**: See `config.example.json` for a complete configuration template.

**4. Chat**

```bash
picoclaw agent -m "What is 2+2?"
```

That's it! You have a working AI assistant in 2 minutes.

---
