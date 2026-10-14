# Manage MCP servers

The binary supports these commands using the existing v3 configuration and MCP runtime:

| Command | Behavior |
|---|---|
| picoclaw mcp add [flags] NAME TARGET [ARGS...] | Add a server; confirm before updating an existing name, or use --force |
| picoclaw mcp update [flags] NAME TARGET [ARGS...] | Update an existing name without prompting |
| picoclaw mcp list [--status] [--timeout 5s] | List configuration; optionally probe enabled servers |
| picoclaw mcp show NAME [--timeout 5s] | Show configuration and tool schemas; disabled servers are shown without connecting |
| picoclaw mcp test NAME [--timeout 5s] | Explicitly probe a server, including a disabled server |
| picoclaw mcp remove NAME | Remove configuration; removing the last server disables MCP integration |
| picoclaw mcp edit | Edit a secure temporary configuration through VISUAL, falling back to EDITOR |

The configuration path follows PICOCLAW_CONFIG, then $PICOCLAW_HOME/config.json, then ~/.picoclaw/config.json.

    picoclaw mcp add --env-file .env.mcp local -- npx -y example-mcp-server
    picoclaw mcp add remote https://example.com/mcp --transport streamable-http
    picoclaw mcp add --transport sse events https://example.com/events
    picoclaw mcp update --env MODE=readonly local -- python -u server.py
    picoclaw mcp update --enabled=false remote https://example.com/mcp
    picoclaw mcp list --status --timeout 3s
    picoclaw mcp show remote
    picoclaw mcp test local --timeout 3s
    VISUAL="code --wait" picoclaw mcp edit

--transport/-t accepts stdio, http, streamable-http and sse. The configured runtime continues using its existing transport behavior. Stdio supports repeatable --env/-e KEY=value and --env-file; the file resolves relative to the workspace, and explicit env values override file values. Remote transports support repeatable --header/-H 'Name: Value' or Name=Value. Prefer an env file for secrets to avoid putting them in shell history.

--deferred and --no-deferred override the discovery default. --enabled=false stores a disabled server. Adding an enabled server enables MCP globally; updates preserve the global state unless --enabled=true is explicit. An update replaces the supplied target and command arguments, preserves unspecified enabled/deferred/transport/env-file settings, and merges named env or header assignments. Changing transport removes settings that apply only to the previous transport. Use edit to remove individual assignments.

CLI options can precede the name or follow the target. For stdio, the first unrecognized option after the executable begins an unchanged argument tail; use -- before the executable to pass all its arguments verbatim, including flags that overlap CLI options.

List without status is read only and does not connect. Live list status and enabled show probe only initialize and tools/list, close their manager afterward, and never call a tool. Test is an explicit connectivity request and can probe a disabled server. Local probes apply the existing workspace/isolation policy and do not retry outside a denied sandbox. HTTP request deadlines also bound SDK cancellation notifications and connection cleanup.

Show/list omit command argument values, env/header values and URL userinfo/query/fragment. Tool metadata is redacted against declared env (including env files), headers and URL credentials independently of the optional LLM sensitive filter. Probe failures report the operation and configuration areas to check, without raw transport bodies or credentials.

Edit inherits the terminal and supports quoted editor commands and arguments. It prepares a v3 copy in a private directory with a 0600 file, validates through the existing loader plus enabled MCP target checks, and saves through the existing config writer. Invalid edits, editor failures and changes detected in the source or security sidecar before saving leave the source intact. Existing unrelated fork settings, multi-key models and security-sidecar credentials remain available in the prepared configuration. Temporary files are removed after editing.
