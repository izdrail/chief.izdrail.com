# Chief - Autonomous PRD Agent
# https://github.com/izdrail/chief

BINARY_NAME    := chief
VERSION        := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT         := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_DATE     := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
BUILD_DIR      := ./build
MAIN_PKG       := ./cmd/chief

# Docker variables
IMAGE_PROD          := izdrail/chief.izdrail.com:latest
IMAGE_DEV           := izdrail/chief.izdrail.com:dev
DOCKERFILE          := Dockerfile
DOCKER_COMPOSE_FILE := docker-compose.yml
CONTAINER_NAME      := chief

# Go tooling
GO      := go
GOFLAGS :=
GOOS    ?= $(shell go env GOOS)
GOARCH  ?= $(shell go env GOARCH)

# Build flags — embed version, commit, and build date
LDFLAGS := -ldflags "\
	-X main.Version=$(VERSION) \
	-X main.Commit=$(COMMIT) \
	-X main.BuildDate=$(BUILD_DATE) \
	-s -w"

# Colors for terminal output
GREEN  := \033[0;32m
YELLOW := \033[0;33m
RESET  := \033[0m

.DEFAULT_GOAL := help
.PHONY: all build install run test test-short test-race lint vet fmt tidy \
        clean snapshot release \
        docker-build docker-build-fresh docker-push \
        docker-up docker-down docker-restart docker-logs docker-db-shell \
        logs check deps help

##
## ── Development ──────────────────────────────────────────────────────────────
##

## all: Tidy, vet, and build
all: tidy vet build

## build: Compile binary to ./build/chief
build:
	@printf "$(GREEN)Building $(BINARY_NAME) $(VERSION)...$(RESET)\n"
	@mkdir -p $(BUILD_DIR)
	$(GO) build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME) $(MAIN_PKG)
	@printf "$(GREEN)Binary: $(BUILD_DIR)/$(BINARY_NAME)$(RESET)\n"

## install: Install binary to $$GOPATH/bin
install:
	@printf "$(GREEN)Installing $(BINARY_NAME)...$(RESET)\n"
	$(GO) install $(LDFLAGS) $(MAIN_PKG)

## run: Build then run the TUI
run: build
	@printf "$(GREEN)Running $(BINARY_NAME)...$(RESET)\n"
	$(BUILD_DIR)/$(BINARY_NAME)

##
## ── Testing & Quality ─────────────────────────────────────────────────────────
##

## test: Run all tests with verbose output
test:
	$(GO) test -v -count=1 ./...

## test-short: Run tests without verbose output
test-short:
	$(GO) test -count=1 ./...

## test-race: Run tests with race detector
test-race:
	$(GO) test -race -count=1 ./...

## test-cover: Run tests and output coverage report
test-cover:
	$(GO) test -coverprofile=coverage.out ./...
	$(GO) tool cover -html=coverage.out -o coverage.html
	@printf "$(GREEN)Coverage report: coverage.html$(RESET)\n"

## lint: Run golangci-lint (requires golangci-lint in PATH)
lint:
	golangci-lint run ./...

## vet: Run go vet
vet:
	$(GO) vet ./...

## fmt: Format all Go source files
fmt:
	$(GO) fmt ./...

## tidy: Tidy and verify go.mod / go.sum
tidy:
	$(GO) mod tidy
	$(GO) mod verify

## deps: Print module dependency graph
deps:
	$(GO) mod graph

## check: Run fmt, vet, lint, and tests in sequence
check: fmt vet lint test-short

##
## ── Release ───────────────────────────────────────────────────────────────────
##

## snapshot: Build snapshot release with goreleaser (no publish)
snapshot:
	goreleaser release --snapshot --clean

## release: Publish release via goreleaser (requires GITHUB_TOKEN)
release:
	goreleaser release --clean

##
## ── Docker ────────────────────────────────────────────────────────────────────
##

## docker-build: Build production Docker image (uses layer cache)
docker-build:
	@printf "$(GREEN)Building Docker image $(IMAGE_PROD)...$(RESET)\n"
	docker build \
		--build-arg VERSION=$(VERSION) \
		--build-arg COMMIT=$(COMMIT) \
		--build-arg BUILD_DATE=$(BUILD_DATE) \
		-t $(IMAGE_PROD) \
		-f $(DOCKERFILE) \
		.

## docker-build-fresh: Build production Docker image with no cache
docker-build-fresh:
	@printf "$(YELLOW)Building Docker image (no cache) $(IMAGE_PROD)...$(RESET)\n"
	docker build \
		--no-cache \
		--pull \
		--build-arg VERSION=$(VERSION) \
		--build-arg COMMIT=$(COMMIT) \
		--build-arg BUILD_DATE=$(BUILD_DATE) \
		-t $(IMAGE_PROD) \
		-f $(DOCKERFILE) \
		.

## docker-build-dev: Build development Docker image
docker-build-dev:
	docker build \
		--build-arg VERSION=$(VERSION)-dev \
		-t $(IMAGE_DEV) \
		-f $(DOCKERFILE) \
		.

## docker-push: Push production image to registry
docker-push:
	docker push $(IMAGE_PROD)

## docker-up: Start application stack with Docker Compose (detached)
docker-up:
	docker compose -f $(DOCKER_COMPOSE_FILE) up -d
	@printf "$(GREEN)Stack is up. Run 'make logs' to follow output.$(RESET)\n"

## docker-down: Stop and remove containers
docker-down:
	docker compose -f $(DOCKER_COMPOSE_FILE) down

## docker-restart: Restart running containers
docker-restart:
	docker compose -f $(DOCKER_COMPOSE_FILE) restart

## docker-logs: Follow Docker Compose log output
docker-logs:
	docker compose -f $(DOCKER_COMPOSE_FILE) logs -f

## logs: Alias for docker-logs
logs: docker-logs

## docker-db-shell: Open SQLite shell inside the running container
docker-db-shell:
	docker exec -it $$(docker compose -f $(DOCKER_COMPOSE_FILE) ps -q $(CONTAINER_NAME)) \
		sqlite3 .chief/chief.db

## docker-shell: Open a bash shell inside the running container
docker-shell:
	docker exec -it $$(docker compose -f $(DOCKER_COMPOSE_FILE) ps -q $(CONTAINER_NAME)) \
		/bin/sh

##
## ── Housekeeping ──────────────────────────────────────────────────────────────
##

## clean: Remove build artifacts, coverage reports, and goreleaser dist/
clean:
	@printf "$(YELLOW)Cleaning build artifacts...$(RESET)\n"
	rm -f $(BINARY_NAME)
	rm -rf $(BUILD_DIR)
	rm -rf dist/
	rm -f coverage.out coverage.html

## help: Show this help message
help:
	@printf "\nUsage: make $(YELLOW)[target]$(RESET)\n"
	@awk '/^##/{sub(/^## /,""); print}' $(MAKEFILE_LIST) | \
		awk -F': ' '/^[a-zA-Z]/{printf "  $(GREEN)%-22s$(RESET) %s\n", $$1, $$2} /^──/{printf "\n%s\n", $$0}'
	@printf "\nVariables:\n"
	@printf "  $(GREEN)%-22s$(RESET) %s\n" "GOOS"   "$(GOOS)"
	@printf "  $(GREEN)%-22s$(RESET) %s\n" "GOARCH" "$(GOARCH)"
	@printf "  $(GREEN)%-22s$(RESET) %s\n" "VERSION" "$(VERSION)"
	@printf "  $(GREEN)%-22s$(RESET) %s\n" "COMMIT"  "$(COMMIT)"
	@printf "\n"
## docker-push: Push production image to registry
docker-push:
	docker push $(IMAGE_PROD)

## publish: Build and push production image to Docker Hub
publish: docker-build docker-push
	@printf "$(GREEN)Published $(IMAGE_PROD) to Docker Hub.$(RESET)\n"

## publish-fresh: Build (no cache) and push production image to Docker Hub
publish-fresh: docker-build-fresh docker-push
	@printf "$(GREEN)Published $(IMAGE_PROD) to Docker Hub (fresh build).$(RESET)\n"