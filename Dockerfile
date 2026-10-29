# syntax=docker/dockerfile:1

FROM golang:1.26-alpine AS build
WORKDIR /src
ENV CGO_ENABLED=0
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN go build -trimpath -ldflags="-s -w -X github.com/builtly/builtly/internal/version.Version=${VERSION}" \
	-o /out/builtly ./cmd/builtly

FROM alpine:3.24
RUN apk add --no-cache ca-certificates tzdata su-exec \
	&& adduser -D -H -u 65532 nonroot \
	&& mkdir -p /data \
	&& chown nonroot:nonroot /data
COPY --from=build /out/builtly /builtly
COPY docker/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh
EXPOSE 8080
# Entrypoint runs as root only long enough to chown the data volume, then drops to nonroot.
ENTRYPOINT ["/entrypoint.sh"]
CMD ["serve"]
