.DEFAULT_GOAL := help
.PHONY: help doctor setup dev build install run serve test race lint fmt tidy bench dry dump schema release clean

help: ## show this help
	@awk 'BEGIN {FS = ":.*?## "; printf "\n\033[1mUsage:\033[0m make \033[36m<target>\033[0m\n\n\033[1mTargets:\033[0m\n"} \
	     /^[a-z]+:.*?## / {printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2} \
	     END {print ""}' $(MAKEFILE_LIST)

doctor: ## check required tools and env
	@bash scripts/doctor.sh

setup: ## check tools and download dependencies
	@bash scripts/setup.sh

dev: build ## open the interactive app (first run walks you through setup)
	@./bin/ideacheck $(ARGS)

build: ## compile bin/ideacheck with version and git SHA injected
	@bash scripts/build.sh

install: ## install ideacheck into GOBIN (users install with install.sh)
	@bash scripts/build.sh install

run: build ## check an idea: make run ARGS='"my idea" -b mock'
	@./bin/ideacheck $(ARGS)

serve: build ## run the local HTTP API
	@./bin/ideacheck serve $(ARGS)

test: ## run unit tests
	@go test ./...

race: ## run unit tests with the race detector
	@go test -race ./...

lint: ## go vet, gofmt check, and golangci-lint when installed
	@bash scripts/lint.sh

fmt: ## format all Go code
	@gofmt -w cmd internal configs

tidy: ## tidy go.mod and go.sum
	@go mod tidy

bench: build ## compare backends on the seed dataset
	@./bin/ideacheck bench $(ARGS)

dry: build ## exercise the bench harness with the mock backend
	@./bin/ideacheck bench --dry-run $(ARGS)

dump: build ## print the effective config files (pass ARGS=DIR to write them)
	@./bin/ideacheck config dump $(ARGS)

schema: ## regenerate schemas/check_result.schema.json from the Go types
	@go run ./cmd/schemagen > schemas/check_result.schema.json && echo "wrote schemas/check_result.schema.json"

release: ## tag a version; github actions builds and publishes it
	@bash scripts/release.sh $(ARGS)

clean: ## remove build output and bench results
	@rm -rf bin bench/results/*.json bench/results/*.csv
