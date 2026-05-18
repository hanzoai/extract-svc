SHELL := /usr/bin/env bash
BIN   := extract
PKG   := github.com/hanzoai/extract/cmd/extract

.PHONY: build run dev test fmt lint tidy clean docker

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o $(BIN) $(PKG)

run: build
	./$(BIN)

dev:
	go run $(PKG)

test:
	go test -race -cover ./...

fmt:
	gofmt -w .
	goimports -w . 2>/dev/null || true

lint:
	golangci-lint run ./...

tidy:
	go mod tidy

clean:
	rm -f $(BIN)

docker:
	docker build -t ghcr.io/hanzoai/extract:dev .

.DEFAULT_GOAL := build
