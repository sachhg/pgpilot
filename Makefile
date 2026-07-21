BINARY  := pgpilot
CMD     := ./cmd/pgpilot
BIN_DIR := bin
COMPOSE := docker compose

.DEFAULT_GOAL := help

.PHONY: help build test lint fmt tidy up down bench smoke itest clean

help: ## Show available targets
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-10s\033[0m %s\n",$$1,$$2}'

build: ## Compile the pgpilot binary into bin/
	@mkdir -p $(BIN_DIR)
	go build -o $(BIN_DIR)/$(BINARY) $(CMD)

test: ## Run all tests with the race detector
	go test -race ./...

lint: ## Run golangci-lint over the module
	golangci-lint run

fmt: ## Format all Go source with gofmt -s
	gofmt -s -w .

tidy: ## Tidy go.mod / go.sum
	go mod tidy

up: ## Bring up the local Postgres primary + replica cluster
	$(COMPOSE) up -d --wait

down: ## Tear the cluster down and delete its volumes
	$(COMPOSE) down -v

bench: ## Run Go benchmarks
	go test -run '^$$' -bench=. -benchmem ./...

smoke: ## Run the cluster smoke test (needs `make up` first)
	go test -tags=integration -count=1 -v ./test/smoke/...

itest: ## Run end-to-end integration tests (needs `make up` first)
	go test -tags=integration -count=1 -v ./test/integration/...

clean: ## Remove build artifacts
	rm -rf $(BIN_DIR)
