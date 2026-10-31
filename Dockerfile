# syntax=docker/dockerfile:1

FROM golang:1.24-bookworm AS build
ARG TARGETOS=linux
ARG TARGETARCH=amd64
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/ibc-ferry .

FROM ubuntu:24.04

RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl passwd \
 && rm -rf /var/lib/apt/lists/* \
 && useradd --system --uid 10001 --home-dir /var/lib/ibc-ferry \
      --create-home --shell /usr/sbin/nologin ferry

COPY --from=build /out/ibc-ferry /usr/local/bin/ibc-ferry

USER 10001:10001
WORKDIR /var/lib/ibc-ferry
EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/ibc-ferry"]
CMD ["--config", "/etc/ibc-ferry/config.toml", "start", "--http-addr", "0.0.0.0:8080"]
