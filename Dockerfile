FROM golang:1-alpine AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal

ARG VERSION=dev
RUN mkdir -p /out /data && \
    CGO_ENABLED=0 go build -trimpath \
    -ldflags="-s -w -X github.com/zzqjlcn/xsh/internal/webui.Version=${VERSION}" \
    -o /out/xsh ./cmd/xsh

FROM scratch

COPY --from=builder /out/xsh /xsh
COPY --from=builder --chown=65532:65532 /data /data
ENV HOME=/data XSH_CONFIG_HOME=/data
USER 65532:65532
WORKDIR /data
VOLUME ["/data"]
EXPOSE 8080
STOPSIGNAL SIGINT
ENTRYPOINT ["/xsh"]
CMD ["web", "--host", "0.0.0.0", "--port", "8080"]
