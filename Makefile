BINARY_NAME := cswap
BUILD_DIR := build
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT := $(shell git rev-parse --verify --quiet HEAD 2>/dev/null || echo "none")
DATE := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -ldflags "-X github.com/blairham/go-claude-swap/internal/cmd.Version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)"

.PHONY: all build clean test test-cover lint fmt vet tidy install check sync check-versions

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

# Toolchain pin invariant. The check itself lives in blairham/pre-commit-hooks
# and is wired up in .pre-commit-config.yaml — there is no local copy.
check-versions:
	@pre-commit run check-go-version-sync --all-files

# Rewrite .tool-versions' golang pin from go.mod (go.mod is authoritative).
#
# Exit 1 means "drift found and fixed" — that is success here. Only 2+ (or a
# missing toolchain) is a real failure, so don't blanket-swallow with `|| true`.
#
# Chicken-and-egg: if .tool-versions pins a Go that asdf has not installed, the
# `go` shim refuses to run and this cannot self-heal. The fallback rewrites the
# pin with awk/sed, which needs no Go at all.
sync:
	@go run github.com/blairham/pre-commit-hooks/cmd/check-go-version-sync@v0.1.0 -fix; \
	status=$$?; \
	if [ $$status -le 1 ]; then exit 0; fi; \
	echo "note: falling back (the pinned toolchain is unavailable to run the checker)"; \
	GO_VERSION=$$(awk '/^go [0-9]/{print $$2; exit}' go.mod); \
	if [ -z "$$GO_VERSION" ]; then echo "ERROR: no go directive in go.mod"; exit 1; fi; \
	case "$$GO_VERSION" in \
		*.*.*) ;; \
		*) echo "ERROR: go.mod pins '$$GO_VERSION' with no patch; set .tool-versions by hand"; exit 1 ;; \
	esac; \
	sed -i '' "s/^golang .*/golang $$GO_VERSION/" .tool-versions; \
	echo "Synced .tool-versions: golang $$GO_VERSION"

proto:
	go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.11
	go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.6.1
	protoc --go_out=. --go_opt=module=github.com/blairham/go-claude-swap \
		--go-grpc_out=. --go-grpc_opt=module=github.com/blairham/go-claude-swap \
		pkg/swapapi/swapapi.proto

check: fmt vet test
