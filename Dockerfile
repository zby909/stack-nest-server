FROM golang:1.27.3-alpine AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . ./
ARG VERSION=dev
ARG REVISION=unknown
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w -X stacknest/server/internal/buildinfo.Version=${VERSION} -X stacknest/server/internal/buildinfo.Revision=${REVISION}" -o /out/stacknest ./cmd/stacknest

FROM alpine:3.24

RUN apk add --no-cache ca-certificates tzdata && \
    addgroup -S stacknest && \
    adduser -S -G stacknest -u 10001 stacknest && \
    mkdir -p /data && \
    chown stacknest:stacknest /data

WORKDIR /opt/stacknest
COPY --from=builder /out/stacknest /usr/local/bin/stacknest
COPY LICENSE NOTICE /usr/share/licenses/stacknest/
COPY internal/dockerrunner/seccomp-LICENSE /usr/share/licenses/stacknest/seccomp-LICENSE
LABEL org.opencontainers.image.licenses="Apache-2.0"

ENV STACKNEST_ADDR=:8080
ENV STACKNEST_DATA_DIR=/data

VOLUME ["/data"]
EXPOSE 8080
USER stacknest

ENTRYPOINT ["stacknest"]
