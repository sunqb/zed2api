# ── Stage 1: Build WebUI ──────────────────────────────────────────────────────
FROM node:22-alpine AS webui-builder

WORKDIR /build/webui

COPY webui/package.json webui/package-lock.json* ./
RUN npm ci --prefer-offline

COPY webui/ ./
RUN npm run build

# ── Stage 2: Build Go binary ──────────────────────────────────────────────────
FROM --platform=$BUILDPLATFORM golang:1.22-alpine AS go-builder

ARG TARGETOS=linux
ARG TARGETARCH=amd64

ENV GOPROXY=https://goproxy.cn,direct
ENV CGO_ENABLED=0

WORKDIR /build

COPY go.mod ./
RUN go mod download

COPY *.go ./

# Copy pre-built WebUI so go:embed picks it up
COPY --from=webui-builder /build/webui/dist ./webui/dist/

RUN GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w" -o zed2api .

# ── Stage 3: Runtime ──────────────────────────────────────────────────────────
FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /app

COPY --from=go-builder /build/zed2api /app/zed2api

EXPOSE 8000

ENTRYPOINT ["/app/zed2api", "serve", "8000"]
