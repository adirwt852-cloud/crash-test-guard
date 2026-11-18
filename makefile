.PHONY: build test lint clean run

BINARY_NAME=crash-test-guard
BUILD_DIR=bin
LDFLAGS=-ldflags="-s -w"

build:
	@mkdir -p $(BUILD_DIR)
	go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME) ./cmd/crash-test-guard

test:
	go test -v ./...

lint:
	golangci-lint run

clean:
	rm -rf $(BUILD_DIR)

run: build
	$(BUILD_DIR)/$(BINARY_NAME) --help