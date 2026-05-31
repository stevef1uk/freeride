.PHONY: all build clean test run

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

# Set up a new machine: build proxy, start it, build gastown, start orchestrator
do_it_all: build
	# Start proxy in background
	./freeride --debug > freeride_live.log 2>&1 &
	# Build gastown
	cd gastown && make install
	# Initialize rig and orchestrator
	cd $${GT_ROOT:-$$HOME/gt} && gt down || true
	cd $${GT_ROOT:-$$HOME/gt} && gt up --orchestrator-only &
	# Wait for orchestrator to boot, then run the simple script (if it exists)
	sleep 5
	@if [ -f "scripts/freeride_proxy_performance.py" ]; then \
		echo "Running performance script..."; \
		python3 scripts/freeride_proxy_performance.py; \
	elif [ -f "gastown/e2e_workflow_test.sh" ]; then \
		echo "Running e2e workflow test script..."; \
		bash gastown/e2e_workflow_test.sh; \
	else \
		echo "Rig initialized! Please run your simple script manually."; \
	fi
