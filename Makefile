BIN := eth-custodial
CLCCORE_BIN := clccore
BUILD_CONF := CGO_ENABLED=1 GOOS=linux GOARCH=amd64
BUILD_COMMIT := $(shell git rev-parse --short HEAD 2> /dev/null)
DEBUG := DEV=true

.PHONY: build build-clccore run run-clccore clean docs gen-service-token build-frontend

clean:
	rm ${BIN} ${BOOTSTRAP_BIN}

build-frontend:
	cd ui/smart-contracts-deployer && pnpm install --frozen-lockfile && pnpm run build

build: build-frontend
	${BUILD_CONF} go build -ldflags="-X main.build=${BUILD_COMMIT} -s -w" -o build/${BIN} cmd/service/*.go
	${BUILD_CONF} go build -ldflags="-X main.build=${BUILD_COMMIT} -s -w" -o build/gen-service-token cmd/gen-service-token/*.go

build-clccore: build-frontend
	${BUILD_CONF} go build -ldflags="-X main.build=${BUILD_COMMIT} -s -w" -o build/${CLCCORE_BIN} cmd/clccore/*.go

run: build-frontend
	${BUILD_CONF} ${DEBUG} go run cmd/clccore/*.go -queries core_queries.sql -federation-queries federated_queries.sql

run-clccore: build-frontend
	${BUILD_CONF} ${DEBUG} go run cmd/clccore/*.go -queries core_queries.sql -federation-queries federated_queries.sql

docs:
	swag fmt --dir internal/api/
	swag init --v3.1 --parseDependency --dir internal/api/ -g swagger.go

gen-service-token:
	${BUILD_CONF} ${DEBUG} go run cmd/gen-service-token/main.go -service ussd-staging
