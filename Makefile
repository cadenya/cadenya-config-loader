.PHONY: build test check fmt release-check snapshot

VERSION ?= dev
GORELEASER ?= goreleaser

build:
	go build -mod=readonly -trimpath -ldflags "-X main.version=$(VERSION)" -o bin/ ./cmd/cadenya-config

test:
	go test -race ./...

check:
	go vet ./...
	go test -race ./...
	go run ./cmd/cadenya-config --directory examples/basic validate

fmt:
	gofmt -w cmd internal

release-check:
	$(GORELEASER) check

snapshot:
	$(GORELEASER) release --snapshot --clean --skip=publish
