GOBASE=$(shell pwd)
GOBIN=$(GOBASE)/bin
SCRIPTS=$(GOBASE)/scripts
TOOLSBIN := $(shell pwd)/.tools

ifndef VERBOSE
# --silent drops the need to prepend `@` to suppress command output.
MAKEFLAGS += --silent
endif

SVCNAME=resizer
AUTH_USERNAME=admin
AUTH_PASSWORD=admin
APP_ENV=test

.PHONY: default
default: help

.PHONY: update-go-deps
update-go-deps: ## Updates golang tools dependencies
	echo "updating go dependencies"
	for m in $$(go list -mod=readonly -m -f '{{ if and (not .Indirect) (not .Main)}}{{.Path}}{{end}}' all); do \
		go get $$m; \
	done
	go mod tidy

.PHONY: build
build: tools ## Builds the service from source
	mkdir -p $(GOBIN)
	go build -o $(GOBIN)/$(SVCNAME) cmd/$(SVCNAME)/main.go

.PHONY: build-prod
build-prod: tools ## Builds the service from source for production
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

.PHONY: lint
lint: tools ## Installs golang linter
	$(TOOLSBIN)/golangci-lint run

.PHONY: test
test: ## Runs tests
	APP_ENV=$(APP_ENV) go test -race ./...

.PHONY: test-coverage
test-coverage: ## Runs tests with coverage
	APP_ENV=$(APP_ENV) go test -race -cover ./...

.PHONY: docker-build
docker-build: deps ## Builds docker image
	docker build -t $(SVCNAME) .

.PHONY: docker-run
docker-run: deps ## Builds and runs docker image
	docker run --rm -p 4000:4000 -e AUTH_USERNAME=$(AUTH_USERNAME) -e AUTH_PASSWORD=$(AUTH_PASSWORD) $(SVCNAME)

$(TOOLSBIN)/golangci-lint:  ## Installs golang linter
	scripts/install-golangci-lint $(TOOLSBIN) v1.64.6

.PHONY: help
help: ## Displays this banner
	@grep -hE '^[/a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-26s\033[0m %s\n", $$1, $$2}'

deps:
	command -v docker &> /dev/null || { echo "Docker is not installed" && exit 1; }
	docker info &> /dev/null || { echo "Docker is not running" && exit 1; }
