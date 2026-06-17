FROM node:22-bookworm-slim AS ui-build

RUN npm install -g pnpm@9

WORKDIR /build

COPY ui/smart-contracts-deployer/package.json \
     ui/smart-contracts-deployer/pnpm-lock.yaml \
     ./ui/smart-contracts-deployer/
RUN cd ui/smart-contracts-deployer && pnpm install --frozen-lockfile

COPY ui/smart-contracts-deployer/ ./ui/smart-contracts-deployer/
RUN mkdir -p internal/static && cd ui/smart-contracts-deployer && pnpm run build

# ── Stage 2: Build Go binary ──────────────────────────────────────────────────
FROM golang:1.26.0-bookworm AS go-build

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

COPY --from=ui-build /build/internal/static/dist ./internal/static/dist

RUN go build -o clccore -ldflags="-X main.build=${BUILD} -s -w" ./cmd/core/

# ── Stage 3: Minimal runtime image ────────────────────────────────────────────
FROM debian:bookworm-slim

ENV DEBIAN_FRONTEND=noninteractive

WORKDIR /service

COPY --from=go-build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=go-build /build/clccore .
COPY --from=go-build /build/migrations ./migrations
COPY --from=go-build /build/core_queries.sql .
COPY --from=go-build /build/federated_queries.sql .

EXPOSE 5003

CMD ["./clccore"]
