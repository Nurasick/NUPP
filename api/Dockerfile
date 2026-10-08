# Multi-stage build: compile in a full Go image, then copy only the binaries
# into a tiny "distroless" image with no shell, package manager or compiler.
# Smaller image, faster pulls, far less attack surface (spec R-DX-4).

FROM golang:1.27-alpine AS build
WORKDIR /src
# Copy go.mod/go.sum first and download modules in their own layer: Docker
# caches it, so editing Go code doesn't re-download every dependency.
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# CGO_ENABLED=0 → fully static binaries that run without libc.
# -trimpath and -s -w strip local paths and debug symbols (smaller, reproducible).
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/seed ./cmd/seed \
 && mkdir -p /out/data/files

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/server /out/seed /app/
# The storage directory is created here owned by the "nonroot" user (uid 65532)
# so that a fresh Docker volume mounted on it inherits writable permissions.
COPY --from=build --chown=65532:65532 /out/data /data
ENV HTTP_ADDR=:8080 STORAGE_DIR=/data/files
EXPOSE 8080
# The image's default user is nonroot: the server never runs as root.
ENTRYPOINT ["/app/server"]
