GO        ?= go
BINARY    := bin/openstream
PKG       := ./...
COMPOSE   := docker compose -f docker-compose.dev.yml

.PHONY: build test test-integration lint fmt vet migrate-up migrate-down dev clean

build:
	$(GO) build -o $(BINARY) ./cmd/openstream

test:
	$(GO) test -race -count=1 $(PKG)

# Requires the dev stack (make dev) or OPENSTREAM_TEST_POSTGRES_DSN pointing at
# a scratch database. Integration tests are skipped when the DSN is unset.
test-integration:
	OPENSTREAM_TEST_POSTGRES_DSN=$${OPENSTREAM_TEST_POSTGRES_DSN:-postgres://openstream:openstream@localhost:5432/openstream_test?sslmode=disable} \
	$(GO) test -race -count=1 -tags=integration $(PKG)

lint:
	golangci-lint run

fmt:
	$(GO) fmt $(PKG)
	gofmt -s -w cmd internal

vet:
	$(GO) vet $(PKG)

migrate-up: build
	$(BINARY) migrate up

migrate-down: build
	$(BINARY) migrate down

dev:
	$(COMPOSE) up -d --wait

dev-down:
	$(COMPOSE) down -v

clean:
	rm -rf bin coverage.txt
