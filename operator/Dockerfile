FROM registry.access.redhat.com/hi/go@sha256:4a7079a5b4ad66d4f00995719ed1e5a68385cb89481bccbe02c26565293ec483 AS builder
ARG TARGETOS
ARG TARGETARCH

ENV GOTOOLCHAIN=auto
WORKDIR /workspace

COPY --chmod=644 go.mod go.sum ./
RUN go mod download

COPY cmd/main.go cmd/main.go
COPY api/ api/
COPY internal/ internal/

RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -a -o manager cmd/main.go

FROM registry.access.redhat.com/hi/static@sha256:f0ee5d3625df237937e7fbf242c8dca3d7c1f5584a059f1410c108b9ff767dc3
WORKDIR /
COPY --from=builder /workspace/manager .
COPY LICENSE /licenses/

LABEL name="Konflux Kube Shard Operator"
LABEL vendor="Red Hat, Inc."
LABEL version="0.1.0"
LABEL release="1"
LABEL summary="Kubernetes API server sharding operator for Konflux"
LABEL description="Operator that offloads CRDs from the main cluster etcd to a secondary aggregated API server backed by Kine"
LABEL maintainer="Konflux CI"
LABEL io.k8s.description="Kubernetes API server sharding operator for Konflux"
LABEL io.k8s.display-name="kube-shard-operator"
LABEL distribution-scope="public"
LABEL url="https://github.com/konflux-ci/kube-shard"
LABEL com.redhat.component="kube-shard-operator"

USER 65532:65532

ENTRYPOINT ["/manager"]
