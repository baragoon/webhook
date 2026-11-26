FROM --platform=$BUILDPLATFORM golang:1.27-alpine@sha256:70bd7ddea8b6cbe5d6943cde279d6b67feb10cc924fb9100d1d8757c1575b280 AS build

WORKDIR /src
COPY . .

ARG TARGETOS
ARG TARGETARCH
ARG VERSION
RUN ldflags="-s -w"; \
    if [ -n "$VERSION" ]; then ldflags="$ldflags -X main.version=${VERSION#v}"; fi; \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="$ldflags" -o /webhook .

FROM alpine:3.23

RUN apk add --no-cache ca-certificates
WORKDIR /etc/webhook
COPY --from=build /webhook /usr/local/bin/webhook
EXPOSE 9000
ENTRYPOINT ["webhook"]
