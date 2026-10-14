import assert from "node:assert/strict"
import { once } from "node:events"
import { createServer } from "node:http"
import { test } from "node:test"

test("image upload preserves the file and cleans an earlier upload when the batch fails", async () => {
  const { uploadChatImages } = await import("../src/api/chat-images.ts")
  const requests = []
  let uploads = 0
  const server = createServer(async (request, response) => {
    let body = Buffer.alloc(0)
    for await (const chunk of request) body = Buffer.concat([body, chunk])
    requests.push({ method: request.method, path: request.url, body })
    if (request.method === "DELETE") {
      response.writeHead(204).end()
      return
    }
    uploads += 1
    if (uploads === 2) {
      response.writeHead(413).end("Image exceeds configured size limit.")
      return
    }
    response.writeHead(201, { "Content-Type": "application/json" }).end(
      JSON.stringify({
        ref: "media://image-one",
        url: "/pico/media/image-one?session_id=images",
        filename: "pixel.png",
        content_type: "image/png",
        size: 4,
      }),
    )
  })
  server.listen(0, "127.0.0.1")
  await once(server, "listening")
  const originalFetch = globalThis.fetch
  globalThis.fetch = (path, options) =>
    originalFetch(
      new URL(path, `http://127.0.0.1:${server.address().port}`),
      options,
    )
  try {
    const file = new File([new Uint8Array([137, 80, 78, 71])], "pixel.png", {
      type: "image/png",
    })
    await assert.rejects(
      uploadChatImages([file, file], "images", new AbortController().signal),
      /configured size limit/,
    )
    assert.equal(requests.length, 3)
    assert.equal(requests[0].path, "/pico/uploads?session_id=images")
    assert.match(requests[0].body.toString("latin1"), /filename="pixel.png"/)
    assert.ok(requests[0].body.includes(Buffer.from([137, 80, 78, 71])))
    assert.equal(requests[2].method, "DELETE")
    assert.equal(requests[2].path, "/pico/media/image-one?session_id=images")
  } finally {
    globalThis.fetch = originalFetch
    await new Promise((resolve) => server.close(resolve))
  }
})
