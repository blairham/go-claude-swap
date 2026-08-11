BINARY_NAME := cswap
BUILD_DIR := build
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT := $(shell git rev-parse --verify --quiet HEAD 2>/dev/null || echo "none")
DATE := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -ldflags "-X github.com/blairham/go-claude-swap/internal/cmd.Version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)"

.PHONY: all build clean test test-cover lint fmt vet tidy install check

all: build

build:
	@mkdir -p $(BUILD_DIR)
	go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME) ./cmd/cswap

install:
	go install $(LDFLAGS) ./cmd/cswap

clean:
	rm -rf $(BUILD_DIR) coverage.out coverage.html
	go clean

test:
	go test -v -race ./...

test-cover:
	go test -v -race -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html

lint:
	go tool golangci-lint run ./...

vet:
	go vet ./...

fmt:
	go tool gofumpt -w .

tidy:
	go mod tidy

check: fmt vet test
