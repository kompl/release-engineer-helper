GOPATH_BIN := $(shell go env GOPATH)/bin

.PHONY: build build-all test test-live lint arch-lint vet

build:
	go build -o bambai ./cmd/bambai

build-all: build
	go build -o rate-limit ./cmd/rate-limit
	go build -o test-history ./cmd/test-history

# Герметичный прогон: пустой GITHUB_TOKEN отключает live-тесты GitHub API
test:
	GITHUB_TOKEN= go test ./...

# Live-тесты включительно (нужен GITHUB_TOKEN в окружении)
test-live:
	go test ./...

lint: vet arch-lint

vet:
	go vet ./...

# Архитектурный линтер: граф зависимостей описан в .go-arch-lint.yml
# Установка: go install github.com/fe3dback/go-arch-lint@latest
arch-lint:
	$(GOPATH_BIN)/go-arch-lint check
