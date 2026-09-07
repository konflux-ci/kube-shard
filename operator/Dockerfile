FROM registry.access.redhat.com/hi/go@sha256:71819dc583899f5a4210c1697c7281a62f0c4bbd40db3e38536f212a724308c6 AS builder
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

FROM registry.access.redhat.com/hi/static@sha256:e6e00bcc3803b2faf7de0b08af2e1b21b155da6c891e153caafd99999c083ee1
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
