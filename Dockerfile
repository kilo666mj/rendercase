FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/rendercase ./cmd/rendercase

FROM alpine:3.22
RUN apk add --no-cache ca-certificates && addgroup -S rendercase && adduser -S -G rendercase rendercase
COPY --from=build /out/rendercase /usr/local/bin/rendercase
RUN mkdir -p /var/lib/rendercase/artifacts && chown -R rendercase:rendercase /var/lib/rendercase
USER rendercase
EXPOSE 18100
ENTRYPOINT ["rendercase"]
