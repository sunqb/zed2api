# ── Stage 1: Build WebUI ──────────────────────────────────────────────────────
FROM node:22-alpine AS webui-builder

WORKDIR /build/webui

COPY webui/package.json webui/package-lock.json* ./
RUN npm ci --prefer-offline

COPY webui/ ./
RUN npm run build

# ── Stage 2: Build Zig binary ─────────────────────────────────────────────────
FROM --platform=$BUILDPLATFORM alpine:3.21 AS zig-builder

ARG TARGETOS=linux
ARG TARGETARCH=amd64
ARG ZIG_VERSION=0.15.2

# Install build deps
RUN apk add --no-cache curl xz

# Download Zig toolchain for the build platform
RUN ARCH=$(uname -m) && \
    ZIG_TARBALL="zig-linux-${ARCH}-${ZIG_VERSION}.tar.xz" && \
    curl -fsSL "https://ziglang.org/builds/${ZIG_TARBALL}" -o /tmp/zig.tar.xz && \
    tar -xJf /tmp/zig.tar.xz -C /opt && \
    mv /opt/zig-linux-${ARCH}-${ZIG_VERSION} /opt/zig && \
    rm /tmp/zig.tar.xz

ENV PATH="/opt/zig:$PATH"

WORKDIR /build

COPY build.zig build.zig.zon ./
COPY src/ ./src/

# Copy pre-built WebUI dist so Zig doesn't need Node at build time
COPY --from=webui-builder /build/webui/dist ./webui/dist/

# Cross-compile for target platform
RUN case "$TARGETARCH" in \
      amd64) ZIG_TARGET="x86_64-linux" ;; \
      arm64) ZIG_TARGET="aarch64-linux" ;; \
      *) echo "Unsupported arch: $TARGETARCH" && exit 1 ;; \
    esac && \
    zig build -Dtarget=${ZIG_TARGET} -Doptimize=ReleaseSafe && \
    cp zig-out/bin/zed2api /build/zed2api

# ── Stage 3: Run ─────────────────────────────────────────────────────────────
# Use alpine (not distroless) because zed2api calls curl at runtime for streaming
FROM alpine:3.21

RUN apk add --no-cache curl ca-certificates && \
    addgroup -S app && adduser -S -G app app

WORKDIR /app

COPY --from=zig-builder /build/zed2api .

USER app

EXPOSE 8000

ENTRYPOINT ["/app/zed2api", "serve", "8000"]
