# Web chat images

Paste an image into the dashboard composer, drop image files onto it, or use **Attach images**. Each image has a preview and a remove button. Text and images are uploaded and sent as one message; an image-only message is supported. A failed or canceled upload keeps the text and local previews for retry and sends no partial message. **Cancel upload** stops the active upload batch.

Images go to the current configured model through the existing provider media pipeline. The dashboard does not substitute `image_model` or a light routing model for an image turn. Choose an OpenAI-compatible model with vision support. When its endpoint explicitly rejects image/vision input, the answer explains how to select a vision-capable model or send text without the images. Image input is never retried as text-only input.

The maximum combined image size for one message comes from `agents.defaults.max_media_size` (default 20 MiB). At most four images can be sent together. The gateway checks file bytes, size, and ownership independently of browser validation. Supported formats are JPEG, PNG, GIF, and WebP; SVG and arbitrary files are rejected. Narrow that list with `allowed_image_types` in a Pico channel's settings:

```json
{
  "allowed_image_types": ["image/jpeg", "image/png"]
}
```

Dashboard uploads and image reads require a valid launcher cookie. The launcher proxies `/pico/uploads` and `/pico/media/{id}` with the gateway's Pico token and strips its own cookie. Direct gateway requests independently require the Pico token and the owning chat session ID. A WebSocket cannot attach another session's upload, arbitrary file paths, remote image URLs, or data URLs.

Files use the existing private temporary media store and its configured cleanup lifetime. Removing a completed upload releases its draft reference; a separately admitted message reference protects an image already being processed. Preview URLs are private and temporary. Activity and final answers use the existing chat protocol; Telegram media routing and audio transcription retain their current behavior.

Focused checks use a PNG fixture and local fake model, without paid provider calls:

```sh
go test -race -tags stdjson ./pkg/agent ./pkg/channels/pico ./web/backend/api ./web/backend/middleware -run 'TestWebImage|TestImageUpload|TestDashboardImageProxy|TestDashboardProtectsHTTP|TestTranscribeAudioInMessage'
cd web/frontend
pnpm test:images
pnpm test:chat
pnpm lint
pnpm build:backend
```
