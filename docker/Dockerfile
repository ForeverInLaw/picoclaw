# ============================================================
# Stage 1: Build the picoclaw binary
# ============================================================
FROM golang:1.27.2-alpine AS builder

RUN apk add --no-cache git make

WORKDIR /src

# Cache dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy source and build
COPY . .
ARG VERSION=dev
ARG VCS_REF=dev
RUN --mount=type=cache,target=/root/.cache/go-build make build VERSION=${VERSION} GIT_COMMIT=${VCS_REF}

# ============================================================
# Stage 2: Minimal runtime image
# ============================================================
FROM alpine:3.23
ARG VERSION=dev
ARG VCS_REF=dev
LABEL org.opencontainers.image.version=${VERSION} org.opencontainers.image.revision=${VCS_REF}

RUN apk add --no-cache ca-certificates tzdata curl bubblewrap

# Health check
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD wget -q --spider http://localhost:18790/health || exit 1

# Copy binary
COPY --from=builder /src/build/picoclaw /usr/local/bin/picoclaw

# Create non-root user and group
RUN addgroup -g 1000 picoclaw && \
    adduser -D -u 1000 -G picoclaw picoclaw && \
    mkdir -p /data && chown picoclaw:picoclaw /data

ENV PICOCLAW_HOME=/data

# Switch to non-root user
USER picoclaw

# Run onboard to create initial directories and config
RUN /usr/local/bin/picoclaw onboard

ENTRYPOINT ["picoclaw"]
CMD ["gateway"]
