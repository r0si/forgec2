# Base images are pinned to the multi-arch manifest digest their tag resolves
# to (supply-chain hygiene: a retagged upstream image cannot change what this
# builds from). Dependabot's docker ecosystem refreshes the digests — do not
# replace them with floating tags.

# Stage 1: Build Vite frontend
# Node 24.15 is the project floor (Vitest 5 needs >=22.12, jsdom 30 needs
# >=24.15). Vite 7 itself only needs 20.19: require(esm) must be enabled by
# default, otherwise loading the ESM-only @vitejs/plugin-react fails with
# ERR_REQUIRE_ESM. The pinned builder image below is newer than the floor.
FROM node:26.12-alpine@sha256:40cd011b55415bc894e45b02fdd5a5dfde056921b7d23a64c74f80497af48797 AS frontend-builder
WORKDIR /build/frontend
COPY frontend/package*.json ./
RUN npm ci
COPY frontend/ .
RUN npm run build

# Stage 2: Build Go binary with embedded frontend
FROM golang:1.27-alpine@sha256:f92b6ef800e499660581efdabdf25d9d817a9d124eaf900924f0504e7e27e12d AS backend-builder
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
# Copy frontend output into embed directory BEFORE copying source
COPY --from=frontend-builder /build/frontend/out /build/internal/webdist/dist
# Copy source code (local webdist/dist not needed in build context)
COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY pkg/ ./pkg/
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o forgec2-server ./cmd/server
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o healthcheck ./cmd/healthcheck
# Seed /data with nonroot ownership in the builder (which has a shell).
# NOTE: no RUN in the distroless stage — it has no shell and the build fails.
# The distroless nonroot user is uid/gid 65532 and does not exist in the
# alpine builder image, so chown numerically instead of by name.
RUN mkdir -p /data/db && chown -R 65532:65532 /data

# Stage 3: Runtime — distroless for minimal attack surface
FROM gcr.io/distroless/static-debian12:latest@sha256:d75cdd72874d4790092fcb1b058493ecf6bb5bf2b2b897045b00ff01d91843f2
LABEL org.opencontainers.image.title="ForgeC2"
LABEL org.opencontainers.image.description="Command & Control Framework"
COPY --from=backend-builder /build/forgec2-server /usr/local/bin/
COPY --from=backend-builder /build/healthcheck /usr/local/bin/healthcheck
# Docker copies image content (including ownership) into a fresh named volume,
# so the server can write its SQLite DB there despite running as nonroot.
# Without this, /data is root-owned and first startup crashes with
# "unable to open database file".
COPY --from=backend-builder --chown=nonroot:nonroot /data /data
EXPOSE 8000 443 8443 53
USER nonroot:nonroot
WORKDIR /home/nonroot
ENTRYPOINT ["forgec2-server"]
CMD ["-config", "/data/config.yaml"]
