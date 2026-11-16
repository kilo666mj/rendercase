FROM --platform=$BUILDPLATFORM golang:1.27.4-alpine@sha256:70bd7ddea8b6cbe5d6943cde279d6b67feb10cc924fb9100d1d8757c1575b280 AS build

ARG TARGETOS
ARG TARGETARCH

WORKDIR /src
RUN apk add --no-cache --upgrade ca-certificates
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} go build \
    -trimpath -ldflags="-s -w" -o /out/rendercase ./cmd/rendercase
RUN mkdir -p /out/rootfs/var/lib/rendercase/artifacts && \
    chown -R 65532:65532 /out/rootfs/var/lib/rendercase

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build --chown=65532:65532 /out/rendercase /rendercase
COPY --from=build --chown=65532:65532 /out/rootfs/var/lib/rendercase /var/lib/rendercase
USER 65532:65532
EXPOSE 18100
ENTRYPOINT ["/rendercase"]
