# Web search

`web_search` keeps the existing title, URL and excerpt result format in direct,
group and allowed Telegram inline requests. Parallel uses the current
[GA Search API](https://docs.parallel.ai/search/search-migration-guide).

Configure `tools.web.parallel` with `enabled: true`, `api_key` or `api_keys`,
and `max_results` (default 5, capped at 10). Keys may also come from
`PICOCLAW_TOOLS_WEB_PARALLEL_API_KEY` or `PICOCLAW_TOOLS_WEB_PARALLEL_API_KEYS`.
`base_url` is optional and normally stays unset.

Configure `tools.web.exa` in the same way, or use `PICOCLAW_TOOLS_WEB_EXA_API_KEY`
and `PICOCLAW_TOOLS_WEB_EXA_API_KEYS`. Exa uses its
[Search API](https://exa.ai/docs/reference/search) with relevant highlights.

Set `tools.web.primary_provider` to `parallel` or `exa` to require that provider.
The other provider is a backup when its enabled credentials are available.
Leaving the selector empty chooses Parallel first, then Exa, then keeps
the existing provider precedence and configuration. A selected provider without
enabled credentials fails clearly during setup. Explicit external provider
configuration takes precedence over `prefer_native`, so the requested service
is available even when the model also supports built-in search.

Search requests respect cancellation and the existing HTTP proxy configuration.
Successful searches call only the selected primary. Network interruptions,
temporary rate limits and server failures can call the backup once within the
same timeout budget. Invalid requests, authentication and exhausted credits or
quota do not trigger the backup. If both fail, the error names both failures.
Automated contracts use temporary HTTP servers; no paid service is called by
the tests. A live check needs an operator's configured search key.
