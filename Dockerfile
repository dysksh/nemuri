# -- build stage --
FROM golang:1.25-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o agent-engine ./cmd/agent-engine

# -- wkhtmltopdf download stage --
# Isolated so the .deb is cached independently of runtime apt packages.
# If GitHub releases become unavailable, replace the URL with a private
# S3/ECR-hosted copy (see KNOWLEDGE.md).
FROM debian:12-slim AS wkhtmltopdf
RUN apt-get update && \
    apt-get install -y --no-install-recommends ca-certificates wget && \
    wget -q -O /tmp/wkhtmltox.deb \
      https://github.com/wkhtmltopdf/packaging/releases/download/0.12.6.1-3/wkhtmltox_0.12.6.1-3.bookworm_amd64.deb && \
    rm -rf /var/lib/apt/lists/*

# -- runtime stage --
FROM debian:12-slim

# Install wkhtmltopdf from cached .deb + runtime dependencies
COPY --from=wkhtmltopdf /tmp/wkhtmltox.deb /tmp/wkhtmltox.deb
RUN apt-get update && \
    apt-get install -y --no-install-recommends \
      ca-certificates \
      fonts-noto-cjk \
      fontconfig \
      libxrender1 \
      libxext6 \
      libx11-6 \
      libjpeg62-turbo \
      libpng16-16 \
      /tmp/wkhtmltox.deb && \
    rm /tmp/wkhtmltox.deb && \
    rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY --from=builder /app/agent-engine .

USER nobody:nogroup

ENTRYPOINT ["/app/agent-engine"]
