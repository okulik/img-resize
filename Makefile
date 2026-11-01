GOBIN=$(shell pwd)/bin
GOLANGCI_LINT_VERSION=v2.15.0
# One folder per version, so changing the version installs it.
TOOLSBIN := $(shell pwd)/.tools/$(GOLANGCI_LINT_VERSION)

ifndef VERBOSE
# --silent drops the need to prepend `@` to suppress command output.
MAKEFLAGS += --silent
endif

SVCNAME=img-resize
APP_ENV=test

.PHONY: default
default: help

.PHONY: update-go-deps
update-go-deps: ## Updates Go module dependencies
	echo "updating go dependencies"
	for m in $$(go list -mod=readonly -m -f '{{ if and (not .Indirect) (not .Main)}}{{.Path}}{{end}}' all); do \
		go get $$m; \
	done
	go mod tidy

.PHONY: build
build: ## Builds the service from source
	mkdir -p $(GOBIN)
	go build -o $(GOBIN)/$(SVCNAME) cmd/$(SVCNAME)/main.go

.PHONY: build-prod
build-prod: ## Builds the service from source for production
	mkdir -p $(GOBIN)
	CGO_ENABLED=0 GOOS=linux go build -o $(GOBIN)/$(SVCNAME) -ldflags="-w -s" cmd/$(SVCNAME)/main.go

.PHONY: tools
tools: $(TOOLSBIN)/golangci-lint ## Installs tools like golang linter
	@echo "done installing tools"

.PHONY: clean
clean: ## Cleans build files and artifacts
	GOBIN=$(GOBIN) go clean ./...
	rm -rfv $(GOBIN)/$(SVCNAME)

.PHONY: run
run: ## Starts the service in dev environment
	go run cmd/$(SVCNAME)/main.go

.PHONY: run-worker
run-worker: ## Starts a worker in dev environment
	SVC_ROLE=worker HTTP_SERVER_PORT=4001 SVC_METRICS_PORT=9092 go run cmd/$(SVCNAME)/main.go

.PHONY: lint
lint: tools ## Runs golang linter
	$(TOOLSBIN)/golangci-lint run

.PHONY: test
test: ## Runs tests, skipping the ones that need Redis
	APP_ENV=$(APP_ENV) go test -race $(TESTFLAGS) ./...

.PHONY: test-redis
test-redis: ## Runs tests against a throwaway Redis container on 127.0.0.1:16380
	docker rm -f img-resize-test-redis > /dev/null 2>&1 || true
	docker run -d --name img-resize-test-redis -p 127.0.0.1:16380:6379 redis:8.10.3-alpine > /dev/null
	status=0; \
	for i in $$(seq 1 50); do \
		docker exec img-resize-test-redis redis-cli ping 2> /dev/null | grep -q PONG && break; \
		sleep 0.2; \
	done; \
	SVC_TEST_REDIS_ADDR=127.0.0.1:16380 $(MAKE) test || status=$$?; \
	docker rm -f img-resize-test-redis > /dev/null 2>&1; \
	exit $$status

.PHONY: test-coverage
test-coverage: ## Runs tests with coverage against a throwaway Redis container
	$(MAKE) test-redis TESTFLAGS=-cover

.PHONY: docker-build
docker-build: deps ## Builds docker image
	docker build -t $(SVCNAME) .

.PHONY: docker-compose-run
docker-compose-run: deps ## Builds and runs docker compose services
	docker compose up -d --build

$(TOOLSBIN)/golangci-lint:  ## Installs golang linter
	scripts/install-golangci-lint $(TOOLSBIN) $(GOLANGCI_LINT_VERSION)

.PHONY: help
help: ## Displays this banner
	@grep -hE '^[/a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-26s\033[0m %s\n", $$1, $$2}'

.PHONY: deps
deps:
	command -v docker > /dev/null 2>&1 || { echo "Docker is not installed" && exit 1; }
	docker info > /dev/null 2>&1 || { echo "Docker is not running" && exit 1; }
