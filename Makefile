# Chief - Autonomous PRD Agent
# https://github.com/minicodemonkey/chief

BINARY_NAME := chief
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
BUILD_DIR := ./build
MAIN_PKG := ./cmd/chief

# Go build flags
LDFLAGS := -ldflags "-X main.Version=$(VERSION)"

.PHONY: all build install test lint clean release snapshot help

all: build

## build: Build the binary
build:
	go build $(LDFLAGS) -o $(BINARY_NAME) $(MAIN_PKG)

## install: Install to $GOPATH/bin
install:
	go install $(LDFLAGS) $(MAIN_PKG)

## test: Run all tests
test:
	go test -v ./...

## test-short: Run tests without verbose output
test-short:
	go test ./...

## lint: Run linters (requires golangci-lint)
lint:
	golangci-lint run ./...

## vet: Run go vet
vet:
	go vet ./...

## fmt: Format code
fmt:
	go fmt ./...

## tidy: Tidy and verify dependencies
tidy:
	go mod tidy
	go mod verify

## clean: Remove build artifacts
clean:
	rm -f $(BINARY_NAME)
	rm -rf $(BUILD_DIR)
	rm -rf dist/

## snapshot: Build snapshot release with goreleaser
snapshot:
	goreleaser release --snapshot --clean

## release: Build release (requires GITHUB_TOKEN)
release:
	goreleaser release --clean

## docker-build: Build the Docker image
docker-build:
	docker-compose build

## docker-build-fresh: Build the Docker image from scratch (no cache)
docker-build-fresh:
	docker-compose build --no-cache

## docker-up: Start the application with Docker Compose
docker-up:
	docker-compose up -d

## docker-down: Stop the Docker application
docker-down:
	docker-compose down

## docker-logs: Show Docker logs
docker-logs:
	docker-compose logs -f

## docker-db-shell: Open a shell to the SQLite database
docker-db-shell:
	docker exec -it $$(docker-compose ps -q chief) sqlite3 .chief/chief.db

## logs: Alias for docker-logs
logs: docker-logs

## run: Build and run the TUI
run: build
	./$(BINARY_NAME)

## help: Show this help
help:
	@echo "Usage: make [target]"
	@echo ""
	@echo "Targets:"
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## /  /'
