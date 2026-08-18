FROM registry.access.redhat.com/hi/go@sha256:ec47f66f5edac60d40b54167f1748eb4c7e941fe1d7d9acba5b55e81c60dbe31 AS builder
ARG TARGETOS
ARG TARGETARCH

ENV GOTOOLCHAIN=auto
WORKDIR /app

COPY main.go .
RUN go mod init webhook-server && \
    go get k8s.io/api/admission/v1 && \
    go get k8s.io/apimachinery/pkg/apis/meta/v1 && \
    go mod tidy && \
    CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -o webhook-server .

FROM registry.access.redhat.com/hi/static@sha256:e6e00bcc3803b2faf7de0b08af2e1b21b155da6c891e153caafd99999c083ee1
COPY --from=builder /app/webhook-server /webhook-server
USER 65532:65532
ENTRYPOINT ["/webhook-server"]
