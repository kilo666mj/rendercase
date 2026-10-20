FROM --platform=$BUILDPLATFORM golang:1.27.3-alpine@sha256:f2c872bcee949b7be9c490efd02b48a8a2fbf947cdf533f1c58525ec9d8170fd AS build

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
