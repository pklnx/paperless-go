# syntax=docker/dockerfile:1

# Build stages run on the native build platform; the Go stage cross-compiles,
# so arm64 images (Apple Silicon) build quickly without emulation.

# Web UI (React). Its output is embedded into the Go binary.
FROM --platform=$BUILDPLATFORM node:26-alpine AS web
WORKDIR /src/web
# Node 25+ no longer bundles corepack. It installs pnpm in the exact version
# and hash from package.json ("packageManager").
RUN npm install -g corepack@0.36.0 && corepack enable
# The Carbon and IBM Plex packages report usage to IBM after install.
# pnpm-workspace.yaml (the overrides the lockfile was made with) blocks their
# install scripts; without it, the install would ignore that list. The
# variable is a second guard.
ENV IBM_TELEMETRY_DISABLED=true
COPY web/package.json web/pnpm-lock.yaml web/pnpm-workspace.yaml ./
RUN pnpm install --frozen-lockfile
COPY web/ ./
RUN pnpm build

# Keep the golang tag equal to the `toolchain` line in go.mod (a toolchain bump
# changes both together): the image sets GOTOOLCHAIN=local, so a floating tag
# could silently build with an older patch release. The check below fails the
# image build on a mismatch. It covers image builds only, not host builds
# (`make build`, scripts that run `go build` on the host).
FROM --platform=$BUILDPLATFORM golang:1.27.3-alpine AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
COPY scripts/check-go-toolchain.sh ./scripts/
RUN sh scripts/check-go-toolchain.sh go.mod \
 && go mod download && go mod verify
COPY . .
COPY --from=web /src/internal/web/ui/dist ./internal/web/ui/dist
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" \
    -o /out/paperless-go ./cmd/paperless-go \
 && mkdir -p /out/data

# Runtime base: Debian, for the OCR tools (OCRmyPDF with Tesseract and
# Ghostscript, poppler-utils, img2pdf, libheif). Pinned by digest of the
# multi-arch index; Dependabot (docker ecosystem) proposes updates.
# --no-install-recommends keeps it small, so libheif's HEVC decoder plugin is
# named explicitly: without it, heif-dec (trixie also ships the old name
# heif-convert) cannot read HEIC. The release build rebuilds this stage and
# the runtime stage every time (no-cache-filters), for the current security
# updates.
FROM debian:trixie-slim@sha256:62bd423aeee912639ca8ee681c2b762cb3e25137e0a92b20be455898a9f034ad AS runtime-base
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
    ca-certificates \
    img2pdf \
    libheif-examples \
    libheif-plugin-libde265 \
    ocrmypdf \
    poppler-utils \
    tesseract-ocr-deu \
    tesseract-ocr-eng \
    tesseract-ocr-osd \
 && rm -rf /var/lib/apt/lists/* \
 && groupadd --system --gid 65532 nonroot \
 && useradd --system --uid 65532 --gid 65532 --home-dir /home/nonroot \
    --create-home --shell /usr/sbin/nologin nonroot

# Tests with the real tools (build tag "tools"), on the runtime's packages, as
# the runtime user. Not part of the image: CI builds this target and runs it
# (docker build --target toolstest -t t . && docker run --rm t). The HEVC
# encoder plugin is only for making HEIC test files.
FROM build AS toolstest-build
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go test -c -tags tools -o /out/process.test ./internal/process

FROM runtime-base AS toolstest
RUN apt-get update \
 && apt-get install -y --no-install-recommends libheif-plugin-x265 \
 && rm -rf /var/lib/apt/lists/*
COPY --from=toolstest-build /out/process.test /usr/local/bin/process.test
USER 65532:65532
WORKDIR /home/nonroot
ENTRYPOINT ["/usr/local/bin/process.test", "-test.v", "-test.run", "^TestTools"]

# Runtime: the default target, so it stays the last stage.
FROM runtime-base AS runtime
COPY --from=build /out/paperless-go /usr/local/bin/paperless-go
COPY --from=build --chown=65532:65532 /out/data /data
ENV PAPERLESS_GO_DATA_DIR=/data
VOLUME ["/data"]
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/paperless-go"]
CMD ["--help"]
