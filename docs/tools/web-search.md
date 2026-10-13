# Web search

`web_search` keeps the existing title, URL and excerpt result format in direct,
group and allowed Telegram inline requests. Parallel uses the current
[GA Search API](https://docs.parallel.ai/search/search-migration-guide).

Configure `tools.web.parallel` with `enabled: true`, `api_key` or `api_keys`,
and `max_results` (default 5, capped at 10). Keys may also come from
`PICOCLAW_TOOLS_WEB_PARALLEL_API_KEY` or `PICOCLAW_TOOLS_WEB_PARALLEL_API_KEYS`.
`base_url` is optional and normally stays unset.

Set `tools.web.primary_provider` to `parallel` to require Parallel. Leaving it
empty chooses Parallel when enabled credentials are available, otherwise keeps
the existing provider precedence and configuration. A selected provider without
enabled credentials fails clearly during setup. Explicit external provider
configuration takes precedence over `prefer_native`, so the requested service
is available even when the model also supports built-in search.

Search requests respect cancellation and the existing HTTP proxy configuration.
Automated contracts use temporary HTTP servers; no paid service is called by
the tests. A live check needs an operator's configured search key.
