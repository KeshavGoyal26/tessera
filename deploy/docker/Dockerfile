# Multi-stage build for tessera (media server with built-in recording) and tessera-loadtest.
#   docker build -f deploy/docker/Dockerfile --target tessera  -t tessera .
#   docker build -f deploy/docker/Dockerfile --target web-demo -t tessera-web-demo .
# Machine-specific build inputs (git-ignored) go in deploy/docker/local/: *.crt, the root
# certificate of a proxy that re-signs HTTPS, trusted by the Go and npm downloads; .npmrc,
# used by npm (scripts/docker.sh copies it there from NPMRC=<path>). GOPROXY and
# NPM_REGISTRY (build args, e.g. from deploy/.env) pick other download mirrors.
ARG GO_VERSION=1.27.3

FROM golang:${GO_VERSION}-bookworm AS build
COPY deploy/docker/local/ /usr/local/share/ca-certificates/
RUN update-ca-certificates
WORKDIR /src
COPY go.mod go.sum ./
ARG GOPROXY
ENV GOPROXY=${GOPROXY:-https://proxy.golang.org,direct}
RUN go mod download
COPY cmd cmd
COPY pkg pkg
COPY gen gen
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X github.com/KeshavGoyal26/tessera/pkg/version.Version=${VERSION}" \
      -o /out/ ./cmd/tessera ./cmd/tessera-loadtest

FROM gcr.io/distroless/static-debian12:nonroot AS tessera
COPY --from=build /out/tessera /usr/local/bin/tessera
EXPOSE 7880/tcp 7881/tcp 7882/udp 3478/udp 3478/tcp 5349/tcp 6789/tcp
ENTRYPOINT ["/usr/local/bin/tessera"]
CMD ["--config", "/etc/tessera/tessera.yaml"]

FROM gcr.io/distroless/static-debian12:nonroot AS loadtest
COPY --from=build /out/tessera-loadtest /usr/local/bin/tessera-loadtest
ENTRYPOINT ["/usr/local/bin/tessera-loadtest"]

# Web demo: build the browser SDK and the demo, serve the static bundle.
FROM node:22-bookworm-slim AS web-build
COPY deploy/docker/local/ /local/
WORKDIR /src
COPY sdk/js sdk/js
COPY examples/web-demo examples/web-demo
ARG NPM_REGISTRY
RUN if ls /local/*.crt >/dev/null 2>&1; then cat /local/*.crt > /local/all.pem; export NODE_EXTRA_CA_CERTS=/local/all.pem; fi \
    && if [ -f /local/.npmrc ]; then cp /local/.npmrc /root/.npmrc; fi \
    && if [ -n "$NPM_REGISTRY" ]; then export npm_config_registry="$NPM_REGISTRY"; fi \
    && cd sdk/js && npm ci && npm run build \
    && cd ../../examples/web-demo && npm ci && npm run build

FROM nginx:1.27-alpine AS web-demo
COPY --from=web-build /src/examples/web-demo/dist /usr/share/nginx/html
EXPOSE 80
