# Web chat activity

The dashboard chat shows provider-supplied reasoning in a collapsible **Thought** block and tool inputs, public results, and failures in separate tool blocks. The answer remains its own message. Providers that do not supply reasoning have no thought block. Reasoning and tool updates appear during streamed turns; non-streamed responses expose the reasoning returned by the provider.

Tool activity follows the existing `ForActivity` / `ForUser` result contract. Output intended only for the model (`ForLLM`) is not displayed. Configured secrets are filtered through the existing runtime filter. Telegram keeps its answer-only reasoning policy, delivery cursor, and tool activity transport.

The gateway uses the existing progress bus and Pico WebSocket. `activity.update` frames carry a stable activity ID, original request ID, kind, state, and public payload. Answer previews use `message.create` / `message.update` with `final: false`; final replies have the canonical delivery ID, `final: true`, and may replace the preview ID. Cancelling a preview leaves its independent activity visible. Native Pico clients accept final answers as agent input and ignore preview and reasoning frames.

On reconnect, the gateway replays the latest snapshots for the active request. The cache retains at most 128 sessions and 64 snapshots per session; older activity can be dropped. A queued request does not discard the active reply before that request starts producing output. Replay upserts by identity and never executes the provider or tools again. The cache is in memory and disappears when the gateway restarts; saved conversation history remains the source for older answers and does not persist activity blocks.

The frontend keeps one chat store in Zustand and reads it through selectors. The existing chat controller owns the socket lifecycle and session history. Replayed activity and final delivery update the same assistant turn, including when its answer was already restored from history.

Focused checks:

```sh
go test -tags stdjson ./pkg/agent ./pkg/channels ./pkg/channels/pico
go test -race -tags stdjson ./pkg/agent ./pkg/channels/pico -run 'TestWebChat|TestReconnectRetainsFinal|TestClientReceivesOnlyFinalAnswer'
cd web/frontend
pnpm test:chat
pnpm lint
pnpm build:backend
```
