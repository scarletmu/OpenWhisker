# OpenWhisker build & deploy targets.
# CGO is required: the SQLite store depends on github.com/mattn/go-sqlite3.

BINARY  := openwhisker
BIN_DIR := bin
CMD     := ./cmd/openwhisker
IMAGE   := openwhisker:local
# Real compose file lives in the git-ignored deploy/local/ (see docs/deployment/README.md).
COMPOSE := deploy/local/compose.yaml

export CGO_ENABLED := 1

.PHONY: build test vet fmt clean docker-build docker-run

## build: compile the openwhisker binary into bin/
build:
	mkdir -p $(BIN_DIR)
	go build -trimpath -o $(BIN_DIR)/$(BINARY) $(CMD)

## test: run the full test suite
test:
	go test ./...

## vet: run go vet across all packages
vet:
	go vet ./...

## fmt: format all Go sources
fmt:
	gofmt -w .

## clean: remove build artifacts
clean:
	rm -rf $(BIN_DIR) dist

## docker-build: build the runtime container image
docker-build:
	docker build -t $(IMAGE) .

## docker-run: bring up the local deploy stack (expects deploy/local/compose.yaml)
docker-run:
	docker compose -f $(COMPOSE) up -d
