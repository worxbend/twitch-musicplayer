# Simple Makefile for a Go project

GOVULNCHECK_VERSION ?= v1.10.0
STATICCHECK_VERSION ?= 2026.2.2
AIR_VERSION ?= v1.67.4

# Build the application
all: build test

build:
	@echo "Building..."
	@go build -o main ./cmd/api

# Run the application
run:
	@go run ./cmd/api

# Test the application
test:
	@echo "Testing..."
	@go test -race ./...

# Fail if any file needs gofmt
fmt-check:
	@test -z "$$(gofmt -l .)" || (gofmt -l .; exit 1)

vet:
	@go vet ./...

staticcheck:
	@go run honnef.co/go/tools/cmd/staticcheck@$(STATICCHECK_VERSION) ./...

# Fail if the Docker images or CI drift from the pinned Go/Node toolchains
toolchains:
	@bash ../scripts/check-toolchains.sh

# Fail if go.mod or go.sum is not tidy
tidy-check:
	@go mod tidy -diff

vulncheck:
	@go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

# Same gates as the CI backend job, in the same order
check: toolchains fmt-check tidy-check vet staticcheck test vulncheck build

# Clean the binary
clean:
	@echo "Cleaning..."
	@rm -f main

# Live Reload
watch:
	@go run github.com/air-verse/air@$(AIR_VERSION)

.PHONY: all build run test toolchains fmt-check tidy-check vet staticcheck vulncheck check clean watch
