.PHONY: all build clean test run do_it_all wait-for-gt-stack

# Default target
all: build

# Build the freeride binary
build:
	go build -o freeride .

# Run the proxy
run: build
	./freeride --debug --allow-local-openai > freeride_live.log 2>&1 &

# Run tests
test:
	go test ./... -v -count=1

# Clean up built binary
clean:
	rm -f freeride

wait-for-gt-stack:
	@bash scripts/wait-for-gt-stack.sh

# Set up a new machine: build Freeride proxy, start it, build gastown, boot town via e2e script.
# Requires .env with API keys. Agents call http://localhost:11434 (Freeride), not the Ollama app.
do_it_all: build
	@test -f .env || (echo "FATAL: create .env from .env.template with API keys before make do_it_all" >&2; exit 1)
	@echo "Starting Freeride proxy (cloud routes on :11434)..."
	@./freeride --debug > freeride_live.log 2>&1 &
	@bash scripts/wait-for-gt-stack.sh --freeride-only
	@echo "Building gastown..."
	@cd gastown && make install
	@gt install $${GT_ROOT:-$$HOME/gt} || true
	@if [ -f "scripts/freeride_proxy_performance.py" ]; then \
		echo "Running performance script..."; \
		python3 scripts/freeride_proxy_performance.py; \
	elif [ -f "gastown/e2e_workflow_test.sh" ]; then \
		echo "Running e2e workflow test script..."; \
		DO_IT_ALL=1 bash gastown/e2e_workflow_test.sh; \
	else \
		echo "Rig initialized! Please run your simple script manually."; \
	fi
