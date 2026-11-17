FROM golang:1.26-alpine AS build
ARG MINIO_RELEASE=RELEASE.2025-10-15T17-29-55Z
RUN CGO_ENABLED=0 GOBIN=/out go install -trimpath github.com/minio/minio@${MINIO_RELEASE}

FROM alpine:3.22
RUN apk add --no-cache ca-certificates
COPY --from=build /out/minio /usr/local/bin/minio
ENV MINIO_UPDATE=off
ENTRYPOINT ["/usr/local/bin/minio"]
CMD ["server", "/data"]
