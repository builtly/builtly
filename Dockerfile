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
RUN apk add --no-cache ca-certificates tzdata \
	&& adduser -D -H -u 65532 nonroot
COPY --from=build /out/builtly /builtly
USER nonroot
EXPOSE 8080
ENTRYPOINT ["/builtly"]
CMD ["serve"]
