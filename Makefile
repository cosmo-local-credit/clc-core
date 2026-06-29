BIN := eth-custodial
CLCCORE_BIN := clccore
BUILD_CONF := CGO_ENABLED=1 GOOS=linux GOARCH=amd64
BUILD_COMMIT := $(shell git rev-parse --short HEAD 2> /dev/null)
DEBUG := DEV=true
ENV_FILE := .env
LOAD_ENV := test ! -f $(ENV_FILE) || . ./$(ENV_FILE)

.PHONY: build build-clccore run run-clccore clean docs gen-service-token build-frontend test test-integration

test:
	go test ./...

# Boots anvil (foundry) forking Celo and relays real multi-call batches through
# the Calibur contract. Requires anvil on PATH; override the fork RPC with
# CELO_FORK_RPC=<url> (use a non-rate-limited node for reliability).
test-integration:
	go test -tags integration -count=1 -timeout 300s ./internal/relayer/

clean:
	rm ${BIN} ${BOOTSTRAP_BIN}

build-frontend:
	cd ui/smart-contracts-deployer && pnpm install --frozen-lockfile && pnpm run build

build: build-frontend
	${BUILD_CONF} go build -ldflags="-X main.build=${BUILD_COMMIT} -s -w" -o build/${BIN} cmd/service/*.go
	${BUILD_CONF} go build -ldflags="-X main.build=${BUILD_COMMIT} -s -w" -o build/gen-service-token cmd/gen-service-token/*.go

build-clccore: build-frontend
	${BUILD_CONF} go build -ldflags="-X main.build=${BUILD_COMMIT} -s -w" -o build/${CLCCORE_BIN} cmd/core/*.go

run: build-frontend
	$(LOAD_ENV) && ${BUILD_CONF} ${DEBUG} go run cmd/core/*.go -queries core_queries.sql -federation-queries federated_queries.sql

run-clccore: build-frontend
	$(LOAD_ENV) && ${BUILD_CONF} ${DEBUG} go run cmd/core/*.go -queries core_queries.sql -federation-queries federated_queries.sql

docs:
	swag fmt --dir internal/api/
	swag init --v3.1 --parseDependency --dir internal/api/ -g swagger.go

gen-service-token:
	$(LOAD_ENV) && ${BUILD_CONF} ${DEBUG} go run cmd/gen-service-token/main.go -service ussd-staging
