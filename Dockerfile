# syntax=docker/dockerfile:1

ARG GO_VERSION=1.27

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS build

WORKDIR /src

ENV CGO_ENABLED=0

COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal
COPY documents ./documents

RUN apk add --no-cache git && go test ./...

ARG TARGETOS
ARG TARGETARCH
RUN GOOS=$TARGETOS GOARCH=$TARGETARCH go build \
    -trimpath \
    -ldflags="-s -w" \
    -o /out/codex-chat \
    ./cmd/codex-chat

RUN GOOS=$TARGETOS GOARCH=$TARGETARCH go build \
    -trimpath \
    -ldflags="-s -w" \
    -o /out/broker-demo \
    ./cmd/broker-demo

RUN GOOS=$TARGETOS GOARCH=$TARGETARCH go build \
    -trimpath \
    -ldflags="-s -w" \
    -o /out/doc-index \
    ./cmd/doc-index

RUN GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/rag-eval ./cmd/rag-eval
RUN GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/rag-bench ./cmd/rag-bench

RUN GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/rag-grounding ./cmd/rag-grounding

RUN GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/rag-dialogue ./cmd/rag-dialogue

RUN GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/project-mcp ./cmd/project-mcp

RUN mkdir -p /out/data && chown 65532:65532 /out/data

FROM alpine:3.23 AS project-mcp
RUN apk add --no-cache git && mkdir /project
COPY --from=build /out/project-mcp /project-mcp
USER 65532:65532
ENTRYPOINT ["/project-mcp"]

FROM scratch AS runtime

COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/codex-chat /codex-chat
COPY --from=build /out/broker-demo /broker-demo
COPY --from=build /out/doc-index /doc-index
COPY --from=build /out/rag-eval /rag-eval
COPY --from=build /out/rag-bench /rag-bench
COPY --from=build /out/rag-grounding /rag-grounding
COPY --from=build /out/rag-dialogue /rag-dialogue
COPY --chown=65532:65532 documents /documents
COPY --chown=65532:65532 artifacts/docindex /documents/artifacts
COPY --chown=65532:65532 artifacts/rag/comparison.json /documents/rag/comparison.json
COPY --chown=65532:65532 artifacts/rag23/comparison.json /documents/rag23/comparison.json
COPY --chown=65532:65532 artifacts/rag24/comparison.json /documents/rag24/comparison.json
COPY --chown=65532:65532 artifacts/rag25 /documents/rag25
COPY --chown=65532:65532 README.md ARCHITECTURE.md MEMORY.md PERSONALIZATION.md TASK_STATE.md INVARIANTS.md RAG.md RAG23.md RAG24.md RAG25.md /project/
COPY --chown=65532:65532 docs /project/docs
COPY --from=build --chown=65532:65532 /out/data /data

ENV DOCUMENT_INDEX_PATH=/documents/artifacts/index.sqlite
ENV RAG_REPORT_PATH=/documents/rag/comparison.json
ENV RAG_EXPERIMENT_PATH=/documents/rag23/comparison.json
ENV RAG_GROUNDING_PATH=/documents/rag24/comparison.json
ENV RAG_DIALOGUE_PATH=/documents/rag25/comparison.json
ENV PROJECT_ROOT=/project

USER 65532:65532

EXPOSE 8080 8090

VOLUME ["/data"]

ENTRYPOINT ["/codex-chat"]
