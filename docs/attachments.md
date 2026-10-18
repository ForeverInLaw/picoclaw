# Incoming file attachments

Incoming source code, text, JSON, CSV and logs are prepared in the agent layer,
independently of the channel and model provider. Telegram preserves the original
filename and MIME type. An ordinary document never selects `image_model` merely
because it is a file; actual image inputs retain the existing vision routing.

New non-image/audio/video attachments are copied into the receiving agent's
`<workspace>/.attachments/` before a model request. Original bytes, metadata and
decoded text views are stored privately. History retains an `attachment://`
reference rather than a temporary channel media reference. Reopening the agent
or clearing the channel media cache does not invalidate that attachment.
References are authorized for the receiving agent and base conversation;
knowing a reference does not grant another conversation access through
`read_file`. As with MCP artifacts, unrestricted host tools remain subject to
the deployment's filesystem and isolation policy.

UTF-8 text (with or without a BOM) and BOM-marked UTF-16 are supported. Files up
to 16 KiB of decoded text are included whole when the assembled request has
room after tools and output reserve. Otherwise the model receives the name,
size, reference and instructions to use the existing paginated `read_file`
tool. No partial file is silently presented as complete. Enable `read_file`
when large text attachments must be analyzed.

Imports enforce the configured `agents.defaults.max_media_size` limit. Original
bytes are preserved even when their format cannot be read as text. Binary
documents, archives and unsupported encodings produce an explicit extraction
notice; PDF/Office conversion and OCR are not implemented by this text-input
path. Files are never executed during preparation.

A missing temporary reference/source or an oversized file is omitted with an
explicit notice to the model; the message text and other valid attachments are
still processed. Storage and security errors stop the request instead.

On Unix, `.attachments/` must be owned by the bot's user or root and must not be
group/world-writable. Unsafe directories are rejected, including reads after
restart; correct ownership and set mode 0700 before retrying. Windows storage
relies on the deployment's ACL policy.

The provider serializer performs no filesystem reads and never interprets
`[file:...]` text as an instruction to attach local bytes. Unsupported input
responses describe a rejected attachment/input format without claiming that
the model lacks vision. Public guidance remains stable through agent error
wrapping; upstream bodies and credentials are not published.

Back up `.attachments/` with the rest of the workspace. Retained originals have
no automatic TTL or disk quota; remove unneeded records only after stopping
writers and accepting that their references will become unavailable. Channel
media references that expired before this change cannot be recovered.
