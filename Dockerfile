FROM --platform=$BUILDPLATFORM golang:1.27.1-alpine@sha256:cf6fca6641884b8433441b2b0652976f975e1d0fdd26d177eaaf8596087f3125 AS build

ARG TARGETOS
ARG TARGETARCH

WORKDIR /src
RUN apk add --no-cache ca-certificates
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
