FROM golang:1.27 AS bridge
WORKDIR /src
COPY bridge/go.mod bridge/go.sum ./
# go.mod replaces the mDNS library with the patched copy in third_party.
COPY bridge/third_party/ ./third_party/
RUN go mod download
COPY bridge/ ./
RUN --mount=type=cache,target=/root/.cache/go-build GOEXPERIMENT=jsonv2 true \
 && GOEXPERIMENT=jsonv2 CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o /quasar-bridge ./cmd/quasar-bridge \
 && GOEXPERIMENT=jsonv2 go tool station-pcm-package -arch arm64 -binary /quasar-bridge

FROM node:25-bookworm-slim AS dependencies
WORKDIR /app/home-functions
RUN npm install -g pnpm@10.23.0
COPY server/package.json server/pnpm-lock.yaml server/pnpm-workspace.yaml ./
RUN pnpm install --frozen-lockfile
COPY server/ ./
# Lint, types and tests run once in the Check workflow, not for every image architecture.
RUN pnpm build

# Trixie for Python 3.13: the Music Assistant player library needs 3.12 or newer.
FROM node:25-trixie-slim
RUN apt-get update && apt-get install -y --no-install-recommends python3 python3-venv openssh-client ca-certificates \
 && rm -rf /var/lib/apt/lists/* \
 && python3 -m venv /app/venv \
 && /app/venv/bin/pip install --no-cache-dir 'websockets>=15.0.1,<16' 'aiosendspin==10.3.0'
# Only the bundled server ships; its build tools stay in the build stage.
COPY --from=dependencies /app/home-functions/dist/main.mjs /app/home-functions/main.mjs
COPY --from=bridge /quasar-bridge /app/artifacts/quasar-bridge
COPY bridge/LICENSE bridge/NOTICE bridge/THIRD_PARTY_NOTICES /app/artifacts/
COPY packaging/ /app/packaging/
COPY manager/ /app/manager/
COPY music/ /app/music/
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
