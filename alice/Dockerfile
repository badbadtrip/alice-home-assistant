FROM golang:1.27 AS bridge
WORKDIR /src
COPY bridge/go.mod bridge/go.sum ./
RUN go mod download
COPY bridge/ ./
RUN --mount=type=cache,target=/root/.cache/go-build GOEXPERIMENT=jsonv2 go test ./internal/indicator ./internal/telemetry ./internal/llm ./internal/home ./internal/bridge ./cmd/quasar-bridge \
 && GOEXPERIMENT=jsonv2 CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o /quasar-bridge ./cmd/quasar-bridge \
 && GOEXPERIMENT=jsonv2 go tool station-pcm-package -arch arm64 -binary /quasar-bridge

# The player and resolver are prebuilt: they need a 32-bit cross toolchain and are tiny.
FROM golang:1.27 AS ytm
WORKDIR /src
COPY ytm/go.mod ytm/go.sum ./
RUN go mod download
COPY ytm/ ./
RUN --mount=type=cache,target=/root/.cache/go-build go test . \
 && mkdir -p /out/bin \
 && CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o /out/bin/ytm-alice . \
 && cp prebuilt/* /out/bin/ && chmod 755 /out/bin/* \
 && cp LICENSE /out/LICENSE \
 && tar -C /out -czf /ytm-alice.tar.gz bin LICENSE

FROM node:25-bookworm-slim AS dependencies
WORKDIR /app/home-functions
RUN npm install -g pnpm@10.23.0
COPY server/package.json server/pnpm-lock.yaml server/pnpm-workspace.yaml ./
RUN pnpm install --frozen-lockfile
COPY server/ ./
RUN pnpm lint && pnpm typecheck && pnpm test

FROM node:25-bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends python3 python3-venv openssh-client ca-certificates \
 && rm -rf /var/lib/apt/lists/* \
 && python3 -m venv /app/venv \
 && /app/venv/bin/pip install --no-cache-dir 'websockets>=15.0.1,<16'
COPY --from=dependencies /app/home-functions /app/home-functions
COPY --from=bridge /quasar-bridge /app/artifacts/quasar-bridge
COPY --from=ytm /ytm-alice.tar.gz /app/artifacts/ytm-alice.tar.gz
COPY bridge/LICENSE bridge/NOTICE bridge/THIRD_PARTY_NOTICES /app/artifacts/
COPY packaging/ /app/packaging/
COPY manager/ /app/manager/
COPY app/ /app/app/
COPY ui/ /app/ui/
COPY wiki/ /app/wiki/
COPY bridge-config.json /app/bridge-config.json
COPY NOTICE /app/NOTICE
ENV PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1
WORKDIR /app
EXPOSE 8099 18080
HEALTHCHECK --interval=30s --timeout=5s --start-period=90s CMD /app/venv/bin/python /app/app/healthcheck.py
CMD ["/app/venv/bin/python", "/app/app/run.py"]
