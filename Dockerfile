FROM golang:1.27.2-alpine AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . ./
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/stacknest ./cmd/stacknest

FROM alpine:3.24

RUN apk add --no-cache ca-certificates tzdata && \
    addgroup -S stacknest && \
    adduser -S -G stacknest -u 10001 stacknest && \
    mkdir -p /data && \
    chown stacknest:stacknest /data

WORKDIR /opt/stacknest
COPY --from=builder /out/stacknest /usr/local/bin/stacknest

ENV STACKNEST_ADDR=:8080
ENV STACKNEST_DATA_DIR=/data

VOLUME ["/data"]
EXPOSE 8080
USER stacknest

ENTRYPOINT ["stacknest"]
