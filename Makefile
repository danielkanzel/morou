BINARY      := router
PKG         := ./...
CMD         := ./cmd/router
BIN_DIR     := bin
IMAGE       := model-router:latest
GO          ?= go

.PHONY: all build run test test-race vet lint fmt tidy clean docker

all: lint test build

build:
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "-s -w" -o $(BIN_DIR)/$(BINARY) $(CMD)

run: build
	$(BIN_DIR)/$(BINARY) --config ./config.yaml

test:
	$(GO) test $(PKG)

test-race:
	$(GO) test -race $(PKG)

vet:
	$(GO) vet $(PKG)

lint:
	golangci-lint run

fmt:
	$(GO) fmt $(PKG)

tidy:
	$(GO) mod tidy

clean:
	rm -rf $(BIN_DIR)

docker:
	docker build -t $(IMAGE) .
