FROM node:24-alpine AS webbuilder
WORKDIR /web
COPY web/package.json web/pnpm-lock.yaml ./
RUN npm install -g pnpm && pnpm install --frozen-lockfile
COPY web/ ./
RUN pnpm build

FROM golang:1.25-alpine AS builder
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=webbuilder /web/dist ./internal/server/web/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o clustr-serverd ./cmd/clustr-serverd

FROM alpine:3.21
# ca-certificates: TLS; rsync/parted/sgdisk/e2fsprogs/xfsprogs/dosfstools: disk imaging utilities
RUN apk add --no-cache ca-certificates rsync parted sgdisk e2fsprogs xfsprogs dosfstools
COPY --from=builder /build/clustr-serverd /usr/local/bin/clustr-serverd
EXPOSE 8080 67/udp 69/udp
VOLUME ["/var/lib/clustr"]
ENTRYPOINT ["clustr-serverd"]
