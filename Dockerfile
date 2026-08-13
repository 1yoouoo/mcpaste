FROM golang:1.26.5-alpine@sha256:0178a641fbb4858c5f1b48e34bdaabe0350a330a1b1149aabd498d0699ff5fb2 AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download
ENV GOFLAGS=-mod=readonly

COPY cmd ./cmd
COPY internal ./internal
COPY db ./db

RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/mcpaste-server ./cmd/server
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/mcpaste-migrate ./cmd/migrate

FROM scratch

COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/mcpaste-server /mcpaste-server
COPY --from=build /out/mcpaste-migrate /mcpaste-migrate

USER 65532:65532

EXPOSE 8080

ENTRYPOINT ["/mcpaste-server"]
