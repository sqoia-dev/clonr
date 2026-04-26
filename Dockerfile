FROM golang:1.25-alpine AS builder
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o clustr-serverd ./cmd/clustr-serverd

FROM alpine:3.19
# ca-certificates: TLS; rsync/parted/sgdisk/e2fsprogs/xfsprogs/dosfstools: disk imaging utilities
RUN apk add --no-cache ca-certificates rsync parted sgdisk e2fsprogs xfsprogs dosfstools
COPY --from=builder /build/clustr-serverd /usr/local/bin/clustr-serverd
EXPOSE 8080 67/udp 69/udp
VOLUME ["/var/lib/clustr"]
ENTRYPOINT ["clustr-serverd"]
