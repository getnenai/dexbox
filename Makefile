.PHONY: all test build-cli install install-cli go-install lint clean help
.PHONY: agent-py-lc-install agent-py-lc-run agent-py-lc-lint agent-ts-vercel-install agent-ts-vercel-run
.PHONY: extend-parse-ts extend-parse-py dexbox-run type-test

all: install-cli ## Default target (build + install to ~/.local/bin)

test: ## Run Go tests
	go test ./...

build-cli: ## Build the Go CLI binary
	mkdir -p bin
	go build -o bin/dexbox ./cmd/dexbox
	@if [ "$$(uname)" = "Darwin" ]; then codesign -f -s - bin/dexbox; fi

install: build-cli ## Install CLI to /usr/local/bin (requires sudo)
	sudo cp bin/dexbox /usr/local/bin/dexbox
	@echo "Installed dexbox to /usr/local/bin"

install-cli: build-cli ## Install CLI to ~/.local/bin
	mkdir -p ~/.local/bin
	cp bin/dexbox ~/.local/bin/dexbox
	@if [ "$$(uname)" = "Darwin" ]; then codesign -f -s - ~/.local/bin/dexbox; fi
	@echo "Installed dexbox to ~/.local/bin"

go-install: ## Install CLI using 'go install' (standard Go way)
	go install ./cmd/dexbox
	@echo "Installed dexbox via 'go install'. Ensure your GOPATH bin is in your PATH."

lint: ## Run go vet on Go sources
	go vet ./...

clean: ## Remove build artifacts
	rm -rf bin/ dist/ build/

dexbox-run: build-cli ## Build and run dexbox (ARGS=... to pass flags, e.g. ARGS="run --type computer --action screenshot")
	./bin/dexbox $(ARGS)

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

agent-ts-vercel-run: ## Run the TypeScript Vercel AI agent (MODEL=optional, PROMPT=optional)
	cd agent/typescript-vercel-ai && npx tsx src/index.ts $(if $(MODEL),--model $(MODEL)) $(if $(PROMPT),"$(PROMPT)")

# --- Extend Parse (shared) ---

extend-parse-ts: ## Parse a document via Extend (TS). FILE=required
	@if [ -z "$(FILE)" ]; then echo "Usage: make extend-parse-ts FILE=myfile.pdf"; exit 1; fi
	cd agent/typescript-vercel-ai && npx tsx ../shared/extend-parse.ts "$(FILE)"

extend-parse-py: ## Parse a document via Extend (Python). FILE=required
	@if [ -z "$(FILE)" ]; then echo "Usage: make extend-parse-py FILE=myfile.pdf"; exit 1; fi
	cd agent/python-langchain && uv run python ../shared/extend_parse.py "$(FILE)"

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-20s\033[0m %s\n", $$1, $$2}'
