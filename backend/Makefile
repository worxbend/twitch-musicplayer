# Simple Makefile for a Go project

GOVULNCHECK_VERSION ?= v1.10.0
AIR_VERSION ?= v1.67.4

# Build the application
all: build test

build:
	@echo "Building..."
	@go build -o main cmd/api/main.go

# Run the application
run:
	@go run cmd/api/main.go

# Test the application
test:
	@echo "Testing..."
	@go test -race ./...

# Fail if any file needs gofmt
fmt-check:
	@test -z "$$(gofmt -l .)" || (gofmt -l .; exit 1)

vet:
	@go vet ./...

vulncheck:
	@go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

# Same gates as the CI backend job, in the same order
check: fmt-check vet test vulncheck build

# Clean the binary
clean:
	@echo "Cleaning..."
	@rm -f main

# Live Reload
watch:
	@go run github.com/air-verse/air@$(AIR_VERSION)

.PHONY: all build run test fmt-check vet vulncheck check clean watch
