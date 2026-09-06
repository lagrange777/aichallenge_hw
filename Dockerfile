# syntax=docker/dockerfile:1

ARG GO_VERSION=1.27

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS build

WORKDIR /src

ENV CGO_ENABLED=0

COPY go.mod ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal

RUN go test ./...

ARG TARGETOS
ARG TARGETARCH
RUN GOOS=$TARGETOS GOARCH=$TARGETARCH go build \
    -trimpath \
    -ldflags="-s -w" \
    -o /out/codex-chat \
    ./cmd/codex-chat

FROM scratch AS runtime

COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/codex-chat /codex-chat

USER 65532:65532

EXPOSE 8080

ENTRYPOINT ["/codex-chat"]
