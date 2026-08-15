FROM golang:1.26.6-alpine@sha256:af8d6740070b8906d12eae1c3e3ea0957fb63f492051ea05e354c38ef9fe88df AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download
ENV GOFLAGS=-mod=readonly

COPY cmd ./cmd
COPY internal ./internal
COPY db ./db

RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/mcpaste-server ./cmd/server
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/mcpaste-migrate ./cmd/migrate
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/mcpaste-healthcheck ./cmd/healthcheck

FROM scratch

COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/mcpaste-server /mcpaste-server
COPY --from=build /out/mcpaste-migrate /mcpaste-migrate
COPY --from=build /out/mcpaste-healthcheck /mcpaste-healthcheck

USER 65532:65532

EXPOSE 8080
VOLUME ["/var/lib/mcpaste/data"]

ENTRYPOINT ["/mcpaste-server"]
