.PHONY: all build clean test run do_it_all wait-for-gt-stack check-do-it-all-deps

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

check-do-it-all-deps:
	@bash scripts/check-do-it-all-deps.sh

# Set up a new machine: build Freeride proxy, start it, build gastown, boot town via e2e script.
# Requires .env with API keys. Agents call http://localhost:11434 (Freeride), not the Ollama app.
# E2e uses FREERIDE_ROOT scripts to avoid NATS/orchestrator races (see scripts/wait-for-gt-stack.sh).
do_it_all: check-do-it-all-deps build
	@echo "Starting Freeride proxy (cloud routes on :11434)..."
	@./freeride --debug > freeride_live.log 2>&1 &
	@bash scripts/wait-for-gt-stack.sh --freeride-only
	@echo "Building gastown..."
	@cd gastown && make install
	@# GUARD: never create a town inside the freeride repo, even if GT_ROOT is polluted
	@_gt_root="$${GT_ROOT:-$$HOME/gt}"; _freeride_root="$$(pwd)"; \
	if [ "$$_gt_root" = "$$_freeride_root" ] || [ "$$_gt_root" = "$$_freeride_root/" ]; then \
		echo "ERROR: GT_ROOT ($$_gt_root) == FREERIDE_ROOT ($$_freeride_root) — refusing to create town inside freeride. Unset GT_ROOT or set GT_ROOT=\$$HOME/gt and retry." >&2; \
		exit 1; \
	fi; \
	case "$$_gt_root" in "$$_freeride_root"/*) \
		echo "ERROR: GT_ROOT ($$_gt_root) is inside FREERIDE_ROOT ($$_freeride_root) — refusing." >&2; exit 1;; \
	esac; \
	if [ -f "$$_gt_root/mayor/town.json" ]; then \
		echo "Gas Town HQ already initialized at $$_gt_root, skipping..."; \
	else \
		gt install "$$_gt_root"; \
	fi
	@if [ -f "scripts/freeride_proxy_performance.py" ]; then \
		echo "Running performance script..."; \
		python3 scripts/freeride_proxy_performance.py; \
	fi
	@if [ -f "gastown/e2e_workflow_test.sh" ]; then \
		echo "Running e2e workflow test script (hardened bootstrap)..."; \
		FREERIDE_ROOT="$$(pwd)" GT_ROOT="$${GT_ROOT:-$$HOME/gt}" DO_IT_ALL=1 \
			bash gastown/e2e_workflow_test.sh; \
	else \
		echo "Rig initialized! Please run your simple script manually."; \
	fi
