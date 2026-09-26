.DEFAULT_GOAL := help
.PHONY: help doctor setup dev build install run serve api up down test race lint core mobile fmt tidy bench dry dump schema release deploy clean

help: ## show this help
	@awk 'BEGIN {FS = ":.*?## "; printf "\n\033[1mUsage:\033[0m make \033[36m<target>\033[0m\n\n\033[1mTargets:\033[0m\n"} \
	     /^[a-z]+:.*?## / {printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2} \
	     END {print ""}' $(MAKEFILE_LIST)

doctor: ## check required tools and env
	@bash scripts/doctor.sh

setup: ## check tools and download dependencies
	@bash scripts/setup.sh

dev: build ## open the interactive app (first run walks you through setup)
	@bash scripts/exec.sh $(ARGS)

build: ## compile bin/ideacheck with version and git SHA injected
	@bash scripts/build.sh

install: ## install ideacheck into GOBIN (users install with install.sh)
	@bash scripts/build.sh install

run: build ## check an idea: make run ARGS='"my idea" -b mock'
	@bash scripts/exec.sh $(ARGS)

serve: build ## run the local HTTP API
	@bash scripts/exec.sh serve $(ARGS)

api: ## run the Sparkjudge hosted API locally: mock judge, no App Attest
	@bash scripts/api.sh $(ARGS)

up: build ## start a local SearXNG in docker, so research can search the web
	@bash scripts/exec.sh search up

down: build ## stop the local SearXNG
	@bash scripts/exec.sh search down

test: ## run unit tests
	@go test ./...

race: ## run unit tests with the race detector
	@go test -race ./...

lint: ## go vet, gofmt check, and golangci-lint when installed
	@bash scripts/lint.sh

core: ## check the public packages import nothing desktop-only and build for iOS and Android
	@bash scripts/core.sh

mobile: ## build build/Sparkcore.xcframework for the iOS app (ARGS=test runs the Swift proof)
	@bash scripts/mobile.sh $(ARGS)

fmt: ## format all Go code
	@gofmt -w cmd internal configs ideacheck judge rubric prompt search store server mobile

tidy: ## tidy go.mod and go.sum
	@go mod tidy

bench: build ## compare backends on the seed dataset
	@bash scripts/exec.sh bench $(ARGS)

dry: build ## exercise the bench harness with the mock backend
	@bash scripts/exec.sh bench --dry-run $(ARGS)

dump: build ## print the effective config files (pass ARGS=DIR to write them)
	@bash scripts/exec.sh config dump $(ARGS)

schema: ## regenerate schemas/check_result.schema.json from the Go types
	@go run ./cmd/schemagen > schemas/check_result.schema.json && echo "wrote schemas/check_result.schema.json"

release: ## tag a version; github actions builds and publishes it
	@bash scripts/release.sh $(ARGS)

deploy: ## deploy the Sparkjudge hosted API to Fly.io (needs the app, volume and secrets first)
	@bash scripts/deploy.sh $(ARGS)

clean: ## remove build output and bench results
	@rm -rf bin bench/results/*.json bench/results/*.csv
