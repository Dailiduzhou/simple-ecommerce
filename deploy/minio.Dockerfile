# The community edition is source-only; do not depend on the retired
# quay.io/minio/minio or Docker Hub minio/minio image repositories.
# Keep the same fixed MinIO release for CI and local development.
FROM golang:1.24-bookworm AS build
RUN CGO_ENABLED=0 GOBIN=/out go install github.com/minio/minio@RELEASE.2025-04-22T22-12-26Z

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata
COPY --from=build /out/minio /usr/local/bin/minio
EXPOSE 9000 9001
ENTRYPOINT ["minio"]
CMD ["server", "/data"]
