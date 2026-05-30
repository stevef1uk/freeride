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
