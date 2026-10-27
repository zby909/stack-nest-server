FROM golang:1.27.2-alpine AS build
WORKDIR /src
COPY deploy/smoke-mail/main.go ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /mailbox main.go

FROM scratch
COPY --from=build /mailbox /mailbox
ENTRYPOINT ["/mailbox"]
