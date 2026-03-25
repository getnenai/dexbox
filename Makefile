.PHONY: all test build-cli install install-cli go-install lint clean help
.PHONY: agent-py-lc-install agent-py-lc-run agent-py-lc-lint agent-ts-vercel-install agent-ts-vercel-run

all: install-cli ## Default target (build + install to ~/.local/bin)

test: ## Run Go tests
	go test ./...

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

go-install: ## Install CLI using 'go install' (standard Go way)
	go install ./cmd/dexbox
	@echo "Installed dexbox via 'go install'. Ensure your GOPATH bin is in your PATH."

lint: ## Run go vet on Go sources
	go vet ./...

clean: ## Remove build artifacts
	rm -rf bin/ dist/ build/

# --- Python LangChain Agent (uv) ---

agent-py-lc-install: ## Install Python LangChain agent dependencies
	cd agent/python-langchain && uv sync

agent-py-lc-run: ## Run the Python LangChain agent
	cd agent/python-langchain && uv run python agent.py

agent-py-lc-lint: ## Lint the Python LangChain agent with ruff
	cd agent/python-langchain && uv run ruff check .

# --- TypeScript Vercel AI Agent (npm) ---

agent-ts-vercel-install: ## Install TypeScript Vercel AI agent dependencies
	cd agent/typescript-vercel-ai && npm ci

agent-ts-vercel-run: ## Run the TypeScript Vercel AI agent (PROMPT=required)
	@if [ -z "$(PROMPT)" ]; then echo "Usage: make agent-ts-vercel-run PROMPT=\"your instruction\""; exit 1; fi
	cd agent/typescript-vercel-ai && npx tsx src/index.ts "$(PROMPT)"

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-20s\033[0m %s\n", $$1, $$2}'
