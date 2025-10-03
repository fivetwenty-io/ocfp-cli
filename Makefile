# OCFP CLI Go Implementation Makefile
# github.com/ocfp/ocfp-cli-go

# Colors for terminal output
GREEN  := \033[1;32m
YELLOW := \033[1;33m
BLUE   := \033[1;34m
CYAN   := \033[1;36m
WHITE  := \033[1;37m
RED    := \033[1;31m
MAGENTA:= \033[1;35m
RESET  := \033[0m

# Default target - show help
.DEFAULT_GOAL := help

# Variables
BINARY_NAME := ocfp
GIT_BRANCH := $(shell git rev-parse --abbrev-ref HEAD 2>/dev/null || echo "unknown")
GIT_SHA := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev/$(GIT_BRANCH)/$(GIT_SHA)")
BUILD_TIME := $(shell date -u '+%Y-%m-%d_%H:%M:%S')
GO_VERSION := $(shell go version | cut -d' ' -f3)
GO_FILES := $(shell find . -name '*.go' -type f -not -path "./vendor/*")

# Build variables
MAIN_PATH := cmd/ocfp/main.go
BUILD_DIR := build
DIST_DIR := dist
COVERAGE_DIR := coverage

# Docker variables
DOCKER_IMAGE := ocfp-cli
DOCKER_TAG := latest
DEV_CONTAINER := ocfp-dev

# LDFLAGS for version info and optimization
LDFLAGS := -ldflags "\
	-s -w \
	-X github.com/ocfp/ocfp-cli-go/internal/version.Version=$(VERSION) \
	-X github.com/ocfp/ocfp-cli-go/internal/version.BuildTime=$(BUILD_TIME) \
	-X github.com/ocfp/ocfp-cli-go/internal/version.GitCommit=$(GIT_SHA) \
	-X github.com/ocfp/ocfp-cli-go/internal/version.GoVersion=$(GO_VERSION)"

##@ General

.PHONY: help
help: ## Display this help message
	@echo "$(BLUE)OCFP CLI Makefile$(RESET)"
	@echo ""
	@awk 'BEGIN {FS = ":.*##"; printf "Usage:\n  make $(CYAN)<target>$(RESET)\n"} /^[a-zA-Z_-]+:.*?##/ { printf "  $(CYAN)%-20s$(RESET) %s\n", $$1, $$2 } /^##@/ { printf "\n$(YELLOW)%s$(RESET)\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Build

.PHONY: build
build: ## Build macOS ARM64 and Linux AMD64 binaries
	@echo "$(GREEN)Building $(BINARY_NAME)...$(RESET)"
	@mkdir -p $(BUILD_DIR)

	@echo "$(WHITE)  Building macOS ARM64...$(RESET)"
	@GOOS=darwin GOARCH=arm64 go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-darwin-arm64 $(MAIN_PATH)

	@echo "$(WHITE)  Building Linux AMD64...$(RESET)"
	@GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-linux-amd64 $(MAIN_PATH)

	@echo "$(GREEN)✓ Build complete:$(RESET)"
	@echo "$(WHITE)  - $(BUILD_DIR)/$(BINARY_NAME)-darwin-arm64$(RESET)"
	@echo "$(WHITE)  - $(BUILD_DIR)/$(BINARY_NAME)-linux-amd64$(RESET)"

.PHONY: build-linux
build-linux: ## Build Linux binary (amd64)
	@echo "$(GREEN)Building $(BINARY_NAME) for Linux amd64...$(RESET)"
	@mkdir -p $(BUILD_DIR)
	@GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-linux-amd64 $(MAIN_PATH)
	@echo "$(GREEN)✓ Linux build complete$(RESET)"

.PHONY: build-windows
build-windows: ## Build Windows binary (amd64)
	@echo "$(GREEN)Building $(BINARY_NAME) for Windows amd64...$(RESET)"
	@mkdir -p $(BUILD_DIR)
	@GOOS=windows GOARCH=amd64 go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-windows-amd64.exe $(MAIN_PATH)
	@echo "$(GREEN)✓ Windows build complete$(RESET)"

.PHONY: build-macos
build-macos: ## Build macOS binary (universal)
	@echo "$(GREEN)Building $(BINARY_NAME) for macOS (universal)...$(RESET)"
	@mkdir -p $(BUILD_DIR)
	@GOOS=darwin GOARCH=amd64 go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-darwin-amd64 $(MAIN_PATH)
	@GOOS=darwin GOARCH=arm64 go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-darwin-arm64 $(MAIN_PATH)
	@lipo -create -output $(BUILD_DIR)/$(BINARY_NAME)-darwin-universal \
		$(BUILD_DIR)/$(BINARY_NAME)-darwin-amd64 \
		$(BUILD_DIR)/$(BINARY_NAME)-darwin-arm64
	@rm $(BUILD_DIR)/$(BINARY_NAME)-darwin-amd64 $(BUILD_DIR)/$(BINARY_NAME)-darwin-arm64
	@echo "$(GREEN)✓ macOS universal build complete$(RESET)"

.PHONY: build-all
build-all: build-linux build-windows build-macos ## Build binaries for all platforms
	@echo "$(GREEN)✓ All platform builds complete$(RESET)"

.PHONY: install
install: build ## Install binary to GOPATH
	@echo "$(GREEN)Installing $(BINARY_NAME)...$(RESET)"
	@go install $(LDFLAGS) $(MAIN_PATH)
	@echo "$(GREEN)✓ Installed to $(GOPATH)/bin/$(BINARY_NAME)$(RESET)"

.PHONY: run
run: build ## Build and run the application
	@echo "$(GREEN)Running $(BINARY_NAME)...$(RESET)"
	@$(BUILD_DIR)/$(BINARY_NAME) $(ARGS)

##@ Testing

.PHONY: test
test: test-unit test-integration test-plugins ## Run all tests
	@echo "$(GREEN)✓ All tests complete$(RESET)"

.PHONY: test-unit
test-unit: ## Run unit tests with coverage
	@echo "$(GREEN)Running unit tests...$(RESET)"
	@mkdir -p $(COVERAGE_DIR)
	@go test -v -race -coverprofile=$(COVERAGE_DIR)/unit.out -covermode=atomic ./internal/...
	@echo "$(GREEN)✓ Unit tests complete$(RESET)"

.PHONY: test-integration
test-integration: ## Run integration tests
	@echo "$(GREEN)Running integration tests...$(RESET)"
	@go test -v -race -tags=integration ./tests/integration/...
	@echo "$(GREEN)✓ Integration tests complete$(RESET)"

.PHONY: test-plugins
test-plugins: ## Run plugin tests
	@echo "$(GREEN)Running plugin tests...$(RESET)"
	@go test -v -race ./pkg/plugins/...
	@echo "$(GREEN)✓ Plugin tests complete$(RESET)"

.PHONY: test-short
test-short: ## Run tests in short mode
	@echo "$(GREEN)Running short tests...$(RESET)"
	@go test -short $(shell go list ./... | grep -v vendor | grep -v tmp)
	@echo "$(GREEN)✓ Short tests complete$(RESET)"

.PHONY: test-race
test-race: ## Run tests with race detector
	@echo "$(GREEN)Running tests with race detector...$(RESET)"
	@go test -race -v $(shell go list ./... | grep -v vendor | grep -v tmp)
	@echo "$(GREEN)✓ Race condition tests complete$(RESET)"

.PHONY: test-all
test-all: test coverage test-race ## Run all tests with coverage and race detection
	@echo "$(GREEN)✓ All tests and coverage complete$(RESET)"

.PHONY: coverage
coverage: ## Generate test coverage report
	@echo "$(GREEN)Generating coverage report...$(RESET)"
	@mkdir -p $(COVERAGE_DIR)
	@go test -coverprofile=$(COVERAGE_DIR)/coverage.out $(shell go list ./... | grep -v vendor | grep -v tmp)
	@go tool cover -func=$(COVERAGE_DIR)/coverage.out
	@echo "$(GREEN)✓ Coverage report generated$(RESET)"

.PHONY: coverage-html
coverage-html: coverage ## Generate and open HTML coverage report
	@echo "$(GREEN)Opening HTML coverage report...$(RESET)"
	@go tool cover -html=$(COVERAGE_DIR)/coverage.out -o $(COVERAGE_DIR)/coverage.html
	@echo "$(GREEN)✓ Coverage report: $(COVERAGE_DIR)/coverage.html$(RESET)"

.PHONY: bench
bench: ## Run benchmarks
	@echo "$(GREEN)Running benchmarks...$(RESET)"
	@go test -bench=. -benchmem ./...
	@echo "$(GREEN)✓ Benchmarks complete$(RESET)"

##@ Code Quality

.PHONY: fmt
fmt: ## Format all Go source files
	@echo "$(GREEN)Formatting code (go fmt)...$(RESET)"
	@go fmt $(shell go list ./... | grep -v vendor | grep -v tmp)
	@echo "$(GREEN)✓ Code formatted$(RESET)"

.PHONY: vet
vet: ## Run go vet on all source files
	@echo "$(GREEN)Vetting code (go vet)...$(RESET)"
	@go vet $(shell go list ./... | grep -v vendor | grep -v tmp)
	@echo "$(GREEN)✓ Vet analysis complete$(RESET)"

.PHONY: lint
lint: ## Run golangci-lint
	@echo "$(GREEN)Running golangci-lint...$(RESET)"
	@command -v golangci-lint >/dev/null 2>&1 || { \
		echo "$(YELLOW)Installing golangci-lint...$(RESET)"; \
		curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $$(go env GOPATH)/bin; \
	}
	@golangci-lint cache clean
	@golangci-lint run --timeout=5m ./...
	@echo "$(GREEN)✓ Lint check complete$(RESET)"

.PHONY: golangci
golangci: lint ## Alias for lint (runs golangci-lint)

.PHONY: golangci-autofix
golangci-autofix: ## Run golangci-lint auto-fixers for specific linters
	@echo "$(GREEN)Running golangci-lint auto-fixers...$(RESET)"
	@command -v golangci-lint >/dev/null 2>&1 || { \
		echo "$(YELLOW)Installing golangci-lint...$(RESET)"; \
		curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $$(go env GOPATH)/bin; \
	}
	@for target in canonicalheader copyloopvar dupword errorlint exptostd fatcontext ginkgolinter godot goheader importas intrange mirror misspell nakedret nlreturn perfsprint protogetter sloglint tagalign usestdlibvars usetesting wsl_v5; do \
		echo "$(WHITE)  Fixing $$target...$(RESET)"; \
		golangci-lint run --max-same-issues 50 --enable-only $$target --fix; \
	done
	@echo "$(GREEN)✓ Auto-fix complete$(RESET)"

.PHONY: staticcheck
staticcheck: ## Run staticcheck static analysis
	@echo "$(GREEN)Running staticcheck...$(RESET)"
	@command -v staticcheck >/dev/null 2>&1 || { \
		echo "$(YELLOW)Installing staticcheck...$(RESET)"; \
		go install honnef.co/go/tools/cmd/staticcheck@latest; \
	}
	@staticcheck $(shell go list ./... | grep -v vendor | grep -v tmp)
	@echo "$(GREEN)✓ Staticcheck analysis complete$(RESET)"

.PHONY: gocyclo
gocyclo: ## Run gocyclo complexity analysis
	@echo "$(GREEN)Running gocyclo complexity analysis...$(RESET)"
	@command -v gocyclo >/dev/null 2>&1 || { \
		echo "$(YELLOW)Installing gocyclo...$(RESET)"; \
		go install github.com/fzipp/gocyclo/cmd/gocyclo@latest; \
	}
	@gocyclo -over 15 $(shell find . -name '*.go' -type f -not -path "./vendor/*" -not -path "./tmp/*")
	@echo "$(GREEN)✓ Gocyclo analysis complete$(RESET)"

.PHONY: ineffassign
ineffassign: ## Run ineffassign to detect ineffectual assignments
	@echo "$(GREEN)Running ineffassign...$(RESET)"
	@command -v ineffassign >/dev/null 2>&1 || { \
		echo "$(YELLOW)Installing ineffassign...$(RESET)"; \
		go install github.com/gordonklaus/ineffassign@latest; \
	}
	@ineffassign $(shell find . -name '*.go' -type f -not -path "./vendor/*" -not -path "./tmp/*")
	@echo "$(GREEN)✓ Ineffassign analysis complete$(RESET)"

.PHONY: errcheck
errcheck: ## Run errcheck to find unchecked errors
	@echo "$(GREEN)Running errcheck...$(RESET)"
	@command -v errcheck >/dev/null 2>&1 || { \
		echo "$(YELLOW)Installing errcheck...$(RESET)"; \
		go install github.com/kisielk/errcheck@latest; \
	}
	@errcheck -ignoretests $(shell go list ./... | grep -v vendor | grep -v tmp)
	@echo "$(GREEN)✓ Errcheck analysis complete$(RESET)"

.PHONY: goimports
goimports: ## Run goimports to check import formatting
	@echo "$(GREEN)Running goimports...$(RESET)"
	@command -v goimports >/dev/null 2>&1 || { \
		echo "$(YELLOW)Installing goimports...$(RESET)"; \
		go install golang.org/x/tools/cmd/goimports@latest; \
	}
	@goimports -l $(shell find . -name '*.go' -type f -not -path "./vendor/*" -not -path "./tmp/*") | (! grep . || (echo "$(RED)✗ Files need goimports formatting$(RESET)" && false))
	@echo "$(GREEN)✓ Goimports check complete$(RESET)"

.PHONY: revive
revive: ## Run revive linter
	@echo "$(GREEN)Running revive linter...$(RESET)"
	@command -v revive >/dev/null 2>&1 || { \
		echo "$(YELLOW)Installing revive...$(RESET)"; \
		go install github.com/mgechev/revive@latest; \
	}
	@revive -config revive.toml -formatter friendly ./...
	@echo "$(GREEN)✓ Revive linter complete$(RESET)"

.PHONY: deadcode
deadcode: ## Run deadcode to find unused code
	@echo "$(GREEN)Running deadcode analysis...$(RESET)"
	@command -v deadcode >/dev/null 2>&1 || { \
		echo "$(YELLOW)Installing deadcode...$(RESET)"; \
		go install golang.org/x/tools/cmd/deadcode@latest; \
	}
	@deadcode -test $(shell go list ./... | grep -v vendor | grep -v tmp) || true
	@echo "$(GREEN)✓ Deadcode analysis complete$(RESET)"

.PHONY: check
check: fmt vet lint staticcheck ## Run basic checks (fmt, vet, lint, staticcheck)
	@echo "$(GREEN)✓ Basic checks passed$(RESET)"

.PHONY: check-all
check-all: fmt vet lint staticcheck revive gocyclo ineffassign errcheck goimports ## Run all code quality checks
	@echo "$(GREEN)✓ All checks passed$(RESET)"

##@ Security

.PHONY: govulncheck
govulncheck: ## Run vulnerability check on dependencies
	@echo "$(GREEN)Checking for vulnerabilities (govulncheck)...$(RESET)"
	@command -v govulncheck >/dev/null 2>&1 || { \
		echo "$(YELLOW)Installing govulncheck...$(RESET)"; \
		go install golang.org/x/vuln/cmd/govulncheck@latest; \
	}
	@govulncheck $(shell go list ./... | grep -v vendor | grep -v tmp)
	@echo "$(GREEN)✓ Vulnerability check complete$(RESET)"

.PHONY: gosec
gosec: ## Run security scanner on source code
	@echo "$(GREEN)Running security scan (gosec)...$(RESET)"
	@command -v gosec >/dev/null 2>&1 || { \
		echo "$(YELLOW)Installing gosec...$(RESET)"; \
		go install github.com/securego/gosec/v2/cmd/gosec@latest; \
	}
	@gosec -quiet -fmt text -exclude-dir=tmp ./...
	@echo "$(GREEN)✓ Security scan complete$(RESET)"

.PHONY: trivy
trivy: ## Run Trivy container and dependency scanner
	@echo "$(GREEN)Running Trivy scan...$(RESET)"
	@command -v trivy >/dev/null 2>&1 || { \
		echo "$(YELLOW)Trivy not found. Please install it:$(RESET)"; \
		echo "$(CYAN)  brew install trivy$(RESET) (macOS)"; \
		echo "$(CYAN)  apt-get install trivy$(RESET) (Debian/Ubuntu)"; \
		echo "$(CYAN)  Or visit: https://aquasecurity.github.io/trivy$(RESET)"; \
		exit 1; \
	}
	@trivy fs --scanners vuln,misconfig,secret --severity HIGH,CRITICAL --skip-dirs vendor,tmp .
	@echo "$(GREEN)✓ Trivy scan complete$(RESET)"

.PHONY: security
security: govulncheck gosec trivy ## Run all security scans
	@echo "$(GREEN)✓ All security scans complete$(RESET)"

##@ Development

.PHONY: build-dev
build-dev: ## Build development container
	@echo "$(GREEN)Building development container...$(RESET)"
	@docker build -f .devcontainer/Dockerfile -t $(DEV_CONTAINER) .
	@echo "$(GREEN)✓ Development container built$(RESET)"

.PHONY: dev
dev: ## Run development container
	@echo "$(GREEN)Starting development container...$(RESET)"
	@docker run -it --rm \
		-v $(PWD):/workspace \
		-v $(HOME)/.ocfp:/root/.ocfp \
		-w /workspace \
		$(DEV_CONTAINER) \
		/bin/bash
	@echo "$(GREEN)✓ Development session ended$(RESET)"

.PHONY: mocks
mocks: ## Generate mock interfaces for testing
	@echo "$(GREEN)Generating mocks...$(RESET)"
	@command -v mockgen >/dev/null 2>&1 || { \
		echo "$(YELLOW)Installing mockgen...$(RESET)"; \
		go install github.com/golang/mock/mockgen@latest; \
	}
	@go generate ./...
	@echo "$(GREEN)✓ Mocks generated$(RESET)"

.PHONY: docs
docs: ## Generate documentation
	@echo "$(GREEN)Generating documentation...$(RESET)"
	@go doc -all > docs/API.md
	@echo "$(GREEN)✓ Documentation generated$(RESET)"

##@ Dependencies

.PHONY: deps
deps: ## Download and verify dependencies
	@echo "$(GREEN)Downloading dependencies...$(RESET)"
	@go mod download
	@go mod verify
	@echo "$(GREEN)✓ Dependencies ready$(RESET)"

.PHONY: deps-update
deps-update: ## Update all dependencies to latest versions
	@echo "$(GREEN)Updating dependencies...$(RESET)"
	@go get -u ./...
	@go mod tidy
	@echo "$(GREEN)✓ Dependencies updated$(RESET)"

.PHONY: deps-tidy
deps-tidy: ## Clean up go.mod and go.sum
	@echo "$(GREEN)Tidying dependencies...$(RESET)"
	@go mod tidy
	@echo "$(GREEN)✓ Dependencies tidied$(RESET)"

##@ Release

.PHONY: build-release
build-release: clean ## Build release binaries with version info
	@echo "$(GREEN)Building release binaries v$(VERSION)...$(RESET)"
	@mkdir -p $(DIST_DIR)
	
	@echo "$(WHITE)  Building Linux amd64...$(RESET)"
	@GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o $(DIST_DIR)/$(BINARY_NAME)-$(VERSION)-linux-amd64 $(MAIN_PATH)
	
	@echo "$(WHITE)  Building Linux arm64...$(RESET)"
	@GOOS=linux GOARCH=arm64 go build $(LDFLAGS) -o $(DIST_DIR)/$(BINARY_NAME)-$(VERSION)-linux-arm64 $(MAIN_PATH)
	
	@echo "$(WHITE)  Building Windows amd64...$(RESET)"
	@GOOS=windows GOARCH=amd64 go build $(LDFLAGS) -o $(DIST_DIR)/$(BINARY_NAME)-$(VERSION)-windows-amd64.exe $(MAIN_PATH)
	
	@echo "$(WHITE)  Building macOS universal...$(RESET)"
	@GOOS=darwin GOARCH=amd64 go build $(LDFLAGS) -o $(DIST_DIR)/$(BINARY_NAME)-darwin-amd64 $(MAIN_PATH)
	@GOOS=darwin GOARCH=arm64 go build $(LDFLAGS) -o $(DIST_DIR)/$(BINARY_NAME)-darwin-arm64 $(MAIN_PATH)
	@lipo -create -output $(DIST_DIR)/$(BINARY_NAME)-$(VERSION)-darwin-universal \
		$(DIST_DIR)/$(BINARY_NAME)-darwin-amd64 \
		$(DIST_DIR)/$(BINARY_NAME)-darwin-arm64
	@rm $(DIST_DIR)/$(BINARY_NAME)-darwin-amd64 $(DIST_DIR)/$(BINARY_NAME)-darwin-arm64
	
	@echo "$(WHITE)  Generating checksums...$(RESET)"
	@cd $(DIST_DIR) && sha256sum $(BINARY_NAME)-$(VERSION)-* > checksums.sha256
	
	@echo "$(GREEN)✓ Release build complete$(RESET)"
	@echo "$(WHITE)  Release artifacts in $(DIST_DIR)/$(RESET)"

.PHONY: shipit
shipit: ## Build release artifacts (requires VERSION env var)
	@echo "$(BLUE)Preparing release...$(RESET)"
	@test -n "$(VERSION)" || { echo "$(RED)ERROR: VERSION not set$(RESET)"; exit 1; }
	@echo "$(GREEN)OK. VERSION=$(VERSION)$(RESET)"
	@$(MAKE) build-release VERSION=$(VERSION)
	@echo ""
	@echo "$(GREEN)━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━$(RESET)"
	@echo "$(WHITE)Release $(VERSION) artifacts ready in $(DIST_DIR)/$(RESET)"
	@echo "$(GREEN)━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━$(RESET)"

##@ Cleanup

.PHONY: clean
clean: ## Clean build artifacts and test cache
	@echo "$(YELLOW)Cleaning up...$(RESET)"
	@rm -rf $(BUILD_DIR) $(DIST_DIR) $(COVERAGE_DIR)
	@rm -f coverage.out coverage.html test.cov
	@go clean -cache -testcache
	@echo "$(GREEN)✓ Cleanup complete$(RESET)"

##@ CI/CD

.PHONY: ci
ci: deps check-all test-all security ## Run full CI pipeline locally
	@echo "$(GREEN)✓ CI pipeline complete$(RESET)"

##@ Info

.PHONY: version
version: ## Display version information
	@echo "$(CYAN)Version Information$(RESET)"
	@echo "$(WHITE)  Version:    $(VERSION)$(RESET)"
	@echo "$(WHITE)  Git Branch: $(GIT_BRANCH)$(RESET)"
	@echo "$(WHITE)  Git Commit: $(GIT_SHA)$(RESET)"
	@echo "$(WHITE)  Build Time: $(BUILD_TIME)$(RESET)"
	@echo "$(WHITE)  Go Version: $(GO_VERSION)$(RESET)"