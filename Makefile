# OCFP CLI Go Implementation Makefile
# github.com/ocfp/ocfp-cli-go

# Variables
BINARY_NAME := ocfp
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
BUILD_TIME := $(shell date -u '+%Y-%m-%d_%H:%M:%S')
GIT_COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
GO_VERSION := $(shell go version | cut -d' ' -f3)

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
	-X github.com/ocfp/ocfp-cli-go/internal/version.GitCommit=$(GIT_COMMIT) \
	-X github.com/ocfp/ocfp-cli-go/internal/version.GoVersion=$(GO_VERSION)"

# Colors for output
RESET := \033[0m
BOLD := \033[1m
RED := \033[31m
GREEN := \033[32m
YELLOW := \033[33m
BLUE := \033[34m
MAGENTA := \033[35m
CYAN := \033[36m
WHITE := \033[37m

# Default target - show help
.DEFAULT_GOAL := help

# Phony targets
.PHONY: help build build-linux build-windows build-macos build-all build-release \
        test-unit test-integration test-plugins tests \
        fmt vet lint check \
        vulncheck trivy security \
        build-dev dev clean

# Help target with categorized, colorized output
help: ## Show this help message
	@echo "$(BOLD)$(CYAN)OCFP CLI - Go Implementation$(RESET)"
	@echo "$(YELLOW)━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━$(RESET)"
	@echo ""
	@echo "$(BOLD)$(GREEN)General$(RESET)"
	@echo "$(WHITE)  make $(GREEN)help$(RESET)              - Show this help message"
	@echo ""
	@echo "$(BOLD)$(BLUE)Build$(RESET)"
	@echo "$(WHITE)  make $(BLUE)build$(RESET)             - Build binary for current platform"
	@echo "$(WHITE)  make $(BLUE)build-linux$(RESET)       - Build Linux binary (amd64)"
	@echo "$(WHITE)  make $(BLUE)build-windows$(RESET)     - Build Windows binary (amd64)"
	@echo "$(WHITE)  make $(BLUE)build-macos$(RESET)       - Build macOS binary (universal)"
	@echo "$(WHITE)  make $(BLUE)build-all$(RESET)         - Build binaries for all platforms"
	@echo "$(WHITE)  make $(BLUE)build-release$(RESET)     - Build release binaries with version info"
	@echo ""
	@echo "$(BOLD)$(MAGENTA)Test$(RESET)"
	@echo "$(WHITE)  make $(MAGENTA)test-unit$(RESET)         - Run unit tests with coverage"
	@echo "$(WHITE)  make $(MAGENTA)test-integration$(RESET)  - Run integration tests"
	@echo "$(WHITE)  make $(MAGENTA)test-plugins$(RESET)      - Run plugin tests"
	@echo "$(WHITE)  make $(MAGENTA)tests$(RESET)             - Run all tests"
	@echo ""
	@echo "$(BOLD)$(YELLOW)Check$(RESET)"
	@echo "$(WHITE)  make $(YELLOW)fmt$(RESET)               - Format code with gofmt"
	@echo "$(WHITE)  make $(YELLOW)vet$(RESET)               - Run go vet for static analysis"
	@echo "$(WHITE)  make $(YELLOW)lint$(RESET)              - Run golangci-lint"
	@echo "$(WHITE)  make $(YELLOW)check$(RESET)             - Run all checks (fmt, vet, lint)"
	@echo ""
	@echo "$(BOLD)$(RED)Security$(RESET)"
	@echo "$(WHITE)  make $(RED)vulncheck$(RESET)         - Run govulncheck for vulnerabilities"
	@echo "$(WHITE)  make $(RED)trivy$(RESET)             - Run trivy security scanner"
	@echo "$(WHITE)  make $(RED)security$(RESET)          - Run all security checks"
	@echo ""
	@echo "$(BOLD)$(CYAN)Development$(RESET)"
	@echo "$(WHITE)  make $(CYAN)build-dev$(RESET)         - Build development container"
	@echo "$(WHITE)  make $(CYAN)dev$(RESET)               - Run development container"
	@echo "$(WHITE)  make $(CYAN)clean$(RESET)             - Clean build artifacts"
	@echo ""
	@echo "$(YELLOW)━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━$(RESET)"
	@echo "$(WHITE)Version: $(VERSION) | Commit: $(GIT_COMMIT)$(RESET)"

# Build targets
build: ## Build binary for current platform
	@echo "$(CYAN)Building $(BINARY_NAME) for current platform...$(RESET)"
	@mkdir -p $(BUILD_DIR)
	@go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME) $(MAIN_PATH)
	@echo "$(GREEN)✓ Build complete: $(BUILD_DIR)/$(BINARY_NAME)$(RESET)"

build-linux: ## Build Linux binary (amd64)
	@echo "$(CYAN)Building $(BINARY_NAME) for Linux amd64...$(RESET)"
	@mkdir -p $(BUILD_DIR)
	@GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-linux-amd64 $(MAIN_PATH)
	@echo "$(GREEN)✓ Linux build complete$(RESET)"

build-windows: ## Build Windows binary (amd64)
	@echo "$(CYAN)Building $(BINARY_NAME) for Windows amd64...$(RESET)"
	@mkdir -p $(BUILD_DIR)
	@GOOS=windows GOARCH=amd64 go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-windows-amd64.exe $(MAIN_PATH)
	@echo "$(GREEN)✓ Windows build complete$(RESET)"

build-macos: ## Build macOS binary (universal)
	@echo "$(CYAN)Building $(BINARY_NAME) for macOS (universal)...$(RESET)"
	@mkdir -p $(BUILD_DIR)
	@GOOS=darwin GOARCH=amd64 go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-darwin-amd64 $(MAIN_PATH)
	@GOOS=darwin GOARCH=arm64 go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-darwin-arm64 $(MAIN_PATH)
	@lipo -create -output $(BUILD_DIR)/$(BINARY_NAME)-darwin-universal \
		$(BUILD_DIR)/$(BINARY_NAME)-darwin-amd64 \
		$(BUILD_DIR)/$(BINARY_NAME)-darwin-arm64
	@rm $(BUILD_DIR)/$(BINARY_NAME)-darwin-amd64 $(BUILD_DIR)/$(BINARY_NAME)-darwin-arm64
	@echo "$(GREEN)✓ macOS universal build complete$(RESET)"

build-all: build-linux build-windows build-macos ## Build binaries for all platforms
	@echo "$(GREEN)✓ All platform builds complete$(RESET)"

build-release: clean ## Build release binaries with version info
	@echo "$(CYAN)Building release binaries v$(VERSION)...$(RESET)"
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

# Test targets
test-unit: ## Run unit tests with coverage
	@echo "$(CYAN)Running unit tests...$(RESET)"
	@mkdir -p $(COVERAGE_DIR)
	@go test -v -race -coverprofile=$(COVERAGE_DIR)/unit.out -covermode=atomic ./internal/...
	@go tool cover -html=$(COVERAGE_DIR)/unit.out -o $(COVERAGE_DIR)/unit.html
	@echo "$(GREEN)✓ Unit tests complete$(RESET)"
	@echo "$(WHITE)  Coverage report: $(COVERAGE_DIR)/unit.html$(RESET)"

test-integration: ## Run integration tests
	@echo "$(CYAN)Running integration tests...$(RESET)"
	@go test -v -race -tags=integration ./tests/integration/...
	@echo "$(GREEN)✓ Integration tests complete$(RESET)"

test-plugins: ## Run plugin tests
	@echo "$(CYAN)Running plugin tests...$(RESET)"
	@go test -v -race ./pkg/plugins/...
	@echo "$(GREEN)✓ Plugin tests complete$(RESET)"

tests: test-unit test-integration test-plugins ## Run all tests
	@echo "$(GREEN)✓ All tests complete$(RESET)"

# Check targets
fmt: ## Format code with gofmt
	@echo "$(CYAN)Formatting code...$(RESET)"
	@gofmt -w -s .
	@echo "$(GREEN)✓ Code formatted$(RESET)"

vet: ## Run go vet for static analysis
	@echo "$(CYAN)Running go vet...$(RESET)"
	@go vet ./...
	@echo "$(GREEN)✓ Vet analysis complete$(RESET)"

lint: ## Run golangci-lint
	@echo "$(CYAN)Running golangci-lint...$(RESET)"
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run --timeout=5m; \
		echo "$(GREEN)✓ Lint check complete$(RESET)"; \
	else \
		echo "$(YELLOW)⚠ golangci-lint not installed$(RESET)"; \
		echo "$(WHITE)  Install with: curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s latest$(RESET)"; \
	fi

check: fmt vet lint ## Run all checks
	@echo "$(GREEN)✓ All checks complete$(RESET)"

# Security targets
vulncheck: ## Run govulncheck for vulnerabilities
	@echo "$(CYAN)Running vulnerability check...$(RESET)"
	@if command -v govulncheck >/dev/null 2>&1; then \
		govulncheck ./...; \
		echo "$(GREEN)✓ Vulnerability check complete$(RESET)"; \
	else \
		echo "$(YELLOW)⚠ govulncheck not installed$(RESET)"; \
		echo "$(WHITE)  Install with: go install golang.org/x/vuln/cmd/govulncheck@latest$(RESET)"; \
	fi

trivy: ## Run trivy security scanner
	@echo "$(CYAN)Running trivy security scan...$(RESET)"
	@if command -v trivy >/dev/null 2>&1; then \
		trivy fs --security-checks vuln,config .; \
		echo "$(GREEN)✓ Security scan complete$(RESET)"; \
	else \
		echo "$(YELLOW)⚠ trivy not installed$(RESET)"; \
		echo "$(WHITE)  Install from: https://github.com/aquasecurity/trivy$(RESET)"; \
	fi

security: vulncheck trivy ## Run all security checks
	@echo "$(GREEN)✓ All security checks complete$(RESET)"

# Development targets
build-dev: ## Build development container
	@echo "$(CYAN)Building development container...$(RESET)"
	@docker build -f .devcontainer/Dockerfile -t $(DEV_CONTAINER) .
	@echo "$(GREEN)✓ Development container built$(RESET)"

dev: ## Run development container
	@echo "$(CYAN)Starting development container...$(RESET)"
	@docker run -it --rm \
		-v $(PWD):/workspace \
		-v $(HOME)/.ocfp:/root/.ocfp \
		-w /workspace \
		$(DEV_CONTAINER) \
		/bin/bash
	@echo "$(GREEN)✓ Development session ended$(RESET)"

# Utility targets
clean: ## Clean build artifacts
	@echo "$(CYAN)Cleaning build artifacts...$(RESET)"
	@rm -rf $(BUILD_DIR) $(DIST_DIR) $(COVERAGE_DIR)
	@go clean -cache -testcache
	@echo "$(GREEN)✓ Clean complete$(RESET)"

# Install target (for local development)
install: build ## Install binary to GOPATH
	@echo "$(CYAN)Installing $(BINARY_NAME)...$(RESET)"
	@go install $(LDFLAGS) $(MAIN_PATH)
	@echo "$(GREEN)✓ Installed to $(GOPATH)/bin/$(BINARY_NAME)$(RESET)"

# Version info
version: ## Show version information
	@echo "$(BOLD)Version Information$(RESET)"
	@echo "$(WHITE)  Version:    $(VERSION)$(RESET)"
	@echo "$(WHITE)  Git Commit: $(GIT_COMMIT)$(RESET)"
	@echo "$(WHITE)  Build Time: $(BUILD_TIME)$(RESET)"
	@echo "$(WHITE)  Go Version: $(GO_VERSION)$(RESET)"

# Dependencies
deps: ## Download and verify dependencies
	@echo "$(CYAN)Downloading dependencies...$(RESET)"
	@go mod download
	@go mod verify
	@echo "$(GREEN)✓ Dependencies ready$(RESET)"

# Run the application
run: build ## Build and run the application
	@echo "$(CYAN)Running $(BINARY_NAME)...$(RESET)"
	@$(BUILD_DIR)/$(BINARY_NAME) $(ARGS)

# Quick test for CI
ci: deps check tests security ## Run CI pipeline locally
	@echo "$(GREEN)✓ CI pipeline complete$(RESET)"

# Generate mocks
mocks: ## Generate mock interfaces for testing
	@echo "$(CYAN)Generating mocks...$(RESET)"
	@if command -v mockgen >/dev/null 2>&1; then \
		go generate ./...; \
		echo "$(GREEN)✓ Mocks generated$(RESET)"; \
	else \
		echo "$(YELLOW)⚠ mockgen not installed$(RESET)"; \
		echo "$(WHITE)  Install with: go install github.com/golang/mock/mockgen@latest$(RESET)"; \
	fi

# Documentation
docs: ## Generate documentation
	@echo "$(CYAN)Generating documentation...$(RESET)"
	@go doc -all > docs/API.md
	@echo "$(GREEN)✓ Documentation generated$(RESET)"

# Benchmark
bench: ## Run benchmarks
	@echo "$(CYAN)Running benchmarks...$(RESET)"
	@go test -bench=. -benchmem ./...
	@echo "$(GREEN)✓ Benchmarks complete$(RESET)"