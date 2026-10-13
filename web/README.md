# Picoclaw Web

This directory contains the standalone web service for `picoclaw`.
It provides a complete unified web interface, acting as a dashboard, configuration center, and interactive console (channel client) for the core `picoclaw` engine.

## Architecture

The service is structured as a monorepo containing both the backend and frontend code to ensure high cohesion and simplify deployment.

*   **`backend/`**: The Go-based web server. It provides RESTful APIs, manages WebSocket connections for chat, and handles the lifecycle of the `picoclaw` process. It eventually embeds the compiled frontend assets into a single executable.
*   **`frontend/`**: The Vite + React + TanStack Router single-page application (SPA). It provides the interactive user interface.

## Getting Started

### Prerequisites

*   Go 1.27.2+
*   Node.js 22.12+ with pnpm

### TypeScript

Builds use the native TypeScript 7 compiler (`pnpm exec tsc --version`).
The `@typescript/native` dependency aliases TypeScript 7, while `typescript`
aliases Microsoft's TypeScript 6 compatibility package for tools such as ESLint
that still require the JavaScript compiler API. This follows Microsoft's
[side-by-side setup](https://devblogs.microsoft.com/typescript/announcing-typescript-7-0/#running-side-by-side-with-typescript-6.0).

From `frontend/`, run a full type check without bundling:

```bash
pnpm exec tsc -b --force
```

### Development

Run both the frontend dev server and the Go backend simultaneously:

```bash
make dev
```

Or run them separately:

```bash
make dev-frontend   # Vite dev server
make dev-backend    # Go backend
```

### Build

Build the frontend and embed it into a single Go binary:

```bash
make build
```

The output binary is `backend/picoclaw-web`.

### Other Commands

```bash
make test    # Run backend tests and frontend lint
make lint    # Run go vet and prettier/eslint
make clean   # Remove all build artifacts
```
