FROM golang:1.26.0-bookworm AS build

ENV CGO_ENABLED=1

ARG BUILDPLATFORM
ARG TARGETPLATFORM
ARG BUILD=dev

WORKDIR /build

RUN echo "Building on $BUILDPLATFORM, building for $TARGETPLATFORM"

COPY go.mod go.sum ./
RUN go mod download

# Copy Go source
COPY . .

RUN go build -o clccore -ldflags="-X main.build=${BUILD} -s -w" ./cmd/core/

# ── Runtime image ─────────────────────────────────────────────────────────────
FROM debian:bookworm-slim

ENV DEBIAN_FRONTEND=noninteractive

WORKDIR /service

COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /build/clccore .
COPY --from=build /build/migrations ./migrations
COPY --from=build /build/core_queries.sql .
COPY --from=build /build/federated_queries.sql .

EXPOSE 5003

CMD ["./clccore"]
