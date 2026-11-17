FROM golang:1.26-alpine AS build
WORKDIR /src
COPY backend/ .
RUN go mod download
RUN CGO_ENABLED=0 go build -mod=mod -trimpath -o /out/server ./cmd/server && CGO_ENABLED=0 go build -mod=mod -trimpath -o /out/worker ./cmd/worker

FROM alpine:3.22
RUN apk add --no-cache ca-certificates  && addgroup -S app && adduser -S -G app app
COPY --from=build /out/server /usr/local/bin/server
COPY --from=build /out/worker /usr/local/bin/worker
USER app
CMD ["/usr/local/bin/server"]
