BIN := eth-custodial
CLCCORE_BIN := clccore
BUILD_CONF := CGO_ENABLED=1 GOOS=linux GOARCH=amd64
BUILD_COMMIT := $(shell git rev-parse --short HEAD 2> /dev/null)
DEBUG := DEV=true
ENV_FILE := .env
LOAD_ENV := test ! -f $(ENV_FILE) || . ./$(ENV_FILE)
CORE_DSN ?= postgres://postgres:postgres@127.0.0.1:5432/clc_core
FEDERATION_DSN ?= postgres://postgres:postgres@127.0.0.1:5433/federation

.PHONY: build build-clccore run run-clccore clean docs gen-service-token test test-integration seed seed-generate

# CORE_TEST_DSN is what makes the catalog search suites run rather than skip. They are the
# only place the query plans are pinned, so a run without it proves less than it appears to.
CORE_TEST_DSN ?= $(CORE_DSN)

# -p 1 because every package's DB suite shares one database. The api suites commit their
# fixtures (they go through the HTTP handlers, which open their own connections) and delete
# them in cleanup, so a store assertion comparing two reads can straddle that and see rows
# appear or vanish between them. No DB test calls t.Parallel, so serializing the packages is
# enough to make the shared database behave like a private one.
test:
	CORE_TEST_DSN="$(CORE_TEST_DSN)" go test -p 1 ./...

test-integration:
	go test -tags integration -count=1 -timeout 300s ./internal/relayer/

clean:
	rm ${BIN} ${BOOTSTRAP_BIN}

build:
	${BUILD_CONF} go build -ldflags="-X main.build=${BUILD_COMMIT} -s -w" -o build/${BIN} cmd/service/*.go
	${BUILD_CONF} go build -ldflags="-X main.build=${BUILD_COMMIT} -s -w" -o build/gen-service-token cmd/gen-service-token/*.go

build-clccore:
	${BUILD_CONF} go build -ldflags="-X main.build=${BUILD_COMMIT} -s -w" -o build/${CLCCORE_BIN} cmd/core/*.go

run:
	$(LOAD_ENV) && ${BUILD_CONF} ${DEBUG} go run cmd/core/*.go -queries core_queries.sql -federation-queries federated_queries.sql

run-clccore:
	$(LOAD_ENV) && ${BUILD_CONF} ${DEBUG} go run cmd/core/*.go -queries core_queries.sql -federation-queries federated_queries.sql

docs:
	swag fmt --dir internal/api/
	swag init --v3.1 --parseDependency --dir internal/api/ -g swagger.go

gen-service-token:
	$(LOAD_ENV) && ${BUILD_CONF} ${DEBUG} go run cmd/gen-service-token/main.go -service ussd-staging

seed-generate:
	FEDERATION_DSN="$(FEDERATION_DSN)" ./seed/generate-federation-import.sh

seed/federation-import.sql:
	FEDERATION_DSN="$(FEDERATION_DSN)" ./seed/generate-federation-import.sh

seed: seed/federation-import.sql
	psql "$(CORE_DSN)" -f seed/federation-import.sql
