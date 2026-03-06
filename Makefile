.PHONY: build build-desktop build-sandbox build-cli test lint clean

# Build both Docker images
build: build-desktop build-sandbox ## Build all Docker images

build-desktop: ## Build the desktop container image
	docker build -t dexbox:latest .

build-sandbox: ## Build the sandbox container image
	docker build -t dexbox-sandbox-python:latest -f Dockerfile.sandbox-python .

build-cli: ## Build the Go CLI binary
	mkdir -p bin
	go build -o bin/dexbox ./cmd/dexbox

install: build-cli ## Install CLI to /usr/local/bin (requires sudo)
	sudo cp bin/dexbox /usr/local/bin/dexbox
	@echo "Installed dexbox to /usr/local/bin"

install-cli: build-cli ## Install CLI to ~/.local/bin
	mkdir -p ~/.local/bin
	cp bin/dexbox ~/.local/bin/dexbox
	@echo "Installed dexbox to ~/.local/bin"
	@echo "IMPORTANT: Ensure ~/.local/bin is in your PATH. If not, add this to your shell profile:"
	@echo 'export PATH="$$HOME/.local/bin:$$PATH"'

go-install: ## Install CLI using 'go install' (standard Go way)
	go install ./cmd/dexbox
	@echo "Installed dexbox via 'go install'. Ensure your GOPATH bin is in your PATH."

test: test-python test-integration ## Run all tests

test-python: ## Run Python unit tests
	uv run pytest tests/ -v

test-integration: ## Run Go integration tests
	go test ./cmd/dexbox/...

lint: lint-python lint-go ## Run all linters

lint-python: ## Lint Python sources
	uv run ruff check src/ tests/

lint-go: ## Run go vet on Go sources
	go vet ./...

clean: ## Remove build artifacts
	rm -rf bin/ dist/ build/ .pytest_cache/ .mypy_cache/ .ruff_cache/
	find . -type d -name "__pycache__" -exec rm -rf {} + 2>/dev/null || true

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-20s\033[0m %s\n", $$1, $$2}'
