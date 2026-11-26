FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build

WORKDIR /src
COPY . .

ARG TARGETOS
ARG TARGETARCH
ARG VERSION
RUN ldflags="-s -w"; \
    if [ -n "$VERSION" ]; then ldflags="$ldflags -X main.version=${VERSION#v}"; fi; \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="$ldflags" -o /webhook .

FROM alpine:3.24@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf

RUN apk add --no-cache ca-certificates
WORKDIR /etc/webhook
COPY --from=build /webhook /usr/local/bin/webhook
EXPOSE 9000
ENTRYPOINT ["webhook"]
