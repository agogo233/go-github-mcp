VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build test vet lint fmt docker

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/go-github-mcp ./cmd/server

test:
	go test -race -covermode=atomic -coverprofile=coverage.out ./...

vet:
	go vet ./...

lint:
	golangci-lint run

fmt:
	gofmt -l -w .

docker:
	docker build --build-arg VERSION=$(VERSION) -t ghcr.io/agogo233/go-github-mcp:latest .
