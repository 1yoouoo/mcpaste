FROM golang:1.26.5-alpine AS build

RUN apk add --no-cache ca-certificates

WORKDIR /src

COPY go.mod ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal

RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/mcpaste-server ./cmd/server

FROM scratch

COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/mcpaste-server /mcpaste-server

USER 65532:65532

EXPOSE 8080

ENTRYPOINT ["/mcpaste-server"]
