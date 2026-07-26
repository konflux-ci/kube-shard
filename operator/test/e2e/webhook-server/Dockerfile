FROM golang:1.26 AS builder
WORKDIR /app
COPY main.go .
RUN go mod init webhook-server && \
    go get k8s.io/api/admission/v1 && \
    go get k8s.io/apimachinery/pkg/apis/meta/v1 && \
    go mod tidy && \
    CGO_ENABLED=0 GOOS=linux go build -o webhook-server .

FROM gcr.io/distroless/static:nonroot
COPY --from=builder /app/webhook-server /webhook-server
USER 65532:65532
ENTRYPOINT ["/webhook-server"]
