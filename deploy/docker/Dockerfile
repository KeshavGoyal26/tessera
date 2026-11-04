# syntax=docker/dockerfile:1
# Multi-stage build for tessera, tessera-recorder and tessera-loadtest.
#   docker build -f deploy/docker/Dockerfile --target tessera  -t tessera .
#   docker build -f deploy/docker/Dockerfile --target recorder -t tessera-recorder .
#   docker build -f deploy/docker/Dockerfile --target web-demo -t tessera-web-demo .
ARG GO_VERSION=1.27.3

FROM golang:${GO_VERSION}-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X github.com/KeshavGoyal26/tessera/pkg/version.Version=${VERSION}" \
      -o /out/ ./cmd/tessera ./cmd/tessera-recorder ./cmd/tessera-loadtest

FROM gcr.io/distroless/static-debian12:nonroot AS tessera
COPY --from=build /out/tessera /usr/local/bin/tessera
EXPOSE 7880/tcp 7881/tcp 7882/udp 3478/udp 3478/tcp 5349/tcp 6789/tcp
ENTRYPOINT ["/usr/local/bin/tessera"]
CMD ["--config", "/etc/tessera/tessera.yaml"]

FROM debian:bookworm-slim AS recorder
RUN apt-get update \
    && apt-get install -y --no-install-recommends ffmpeg ca-certificates \
    && apt-get clean \
    && useradd -r -u 65532 tessera \
    && mkdir -p /var/lib/tessera-recorder \
    && chown tessera /var/lib/tessera-recorder
COPY --from=build /out/tessera-recorder /usr/local/bin/tessera-recorder
USER tessera
EXPOSE 6790/tcp
ENTRYPOINT ["/usr/local/bin/tessera-recorder"]
CMD ["--config", "/etc/tessera/recorder.yaml"]

FROM gcr.io/distroless/static-debian12:nonroot AS loadtest
COPY --from=build /out/tessera-loadtest /usr/local/bin/tessera-loadtest
ENTRYPOINT ["/usr/local/bin/tessera-loadtest"]

# Web demo: build the browser SDK and the demo, serve the static bundle.
FROM node:22-bookworm-slim AS web-build
WORKDIR /src
COPY sdk/js sdk/js
COPY examples/web-demo examples/web-demo
RUN cd sdk/js && npm ci && npm run build \
    && cd ../../examples/web-demo && npm ci && npm run build

FROM nginx:1.27-alpine AS web-demo
COPY --from=web-build /src/examples/web-demo/dist /usr/share/nginx/html
EXPOSE 80
