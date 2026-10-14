# Docker Hub publishes only a moving `latest` tag for the community server and
# no versioned release tags, so this build exists to pin a specific release.
# The compose files normally run the digest-pinned image instead; this path
# needs a Go toolchain and is for CI or a reproducible upgrade.
FROM golang:1.24-bookworm AS build
# proxy.golang.org is unreachable from some build hosts; mirror the setting used
# by app/mall/Dockerfile so the MinIO source build resolves modules reliably.
ENV GOPROXY=https://goproxy.cn,direct
RUN CGO_ENABLED=0 GOBIN=/out go install github.com/minio/minio@RELEASE.2025-04-22T22-12-26Z

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata
COPY --from=build /out/minio /usr/local/bin/minio
EXPOSE 9000 9001
ENTRYPOINT ["minio"]
CMD ["server", "/data"]
