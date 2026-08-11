# Build from the repo root: docker build -f cmd/api/Dockerfile .
FROM golang:1.26-alpine AS build
WORKDIR /src

# Preserve the go.work workspace layout so cmd/api's `replace` directive to
# ../../pkg/retrieval resolves exactly like it does in local development —
# same build mode used throughout this project so far, not a special case
# just for Docker.
COPY go.work go.work.sum ./
COPY pkg ./pkg
COPY cmd/indexer ./cmd/indexer
COPY cmd/api ./cmd/api

WORKDIR /src/cmd/api
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/api .

FROM alpine:3.20
# ca-certificates: needed to verify TLS for outbound calls to GitHub,
# Anthropic, and the embedding provider's APIs.
RUN apk add --no-cache ca-certificates
COPY --from=build /out/api /usr/local/bin/api
WORKDIR /app
EXPOSE 3000
ENTRYPOINT ["/usr/local/bin/api"]
