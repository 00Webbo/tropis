# Tropis developer tasks. `make` or `make help` lists them.
#
# Recipes use bash, so they behave the same on Linux, macOS, and Git Bash on
# Windows. Targets needing Docker, kind or a cluster say so in their help.

SHELL := bash
.SHELLFLAGS := -eu -o pipefail -c
.DEFAULT_GOAL := help

GO      ?= go
EXE     := $(shell $(GO) env GOEXE)
BIN     := bin
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/00Webbo/tropis/pkg/reason.AgentVersion=$(VERSION)
IMAGE   ?= tropis:dev
CHART   := deploy/helm/tropis
PKGS    := cmd pkg eval hack deploy

# eval / capture parameters
CORPUS  ?= eval/fixtures
BACKEND ?=
SEED    ?=
REQUIRE_DUST ?= 0

##@ Build

.PHONY: build
build: ## Build tropis and tropis-collector into bin/
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN)/tropis$(EXE) ./cmd/tropis
	$(GO) build -trimpath -ldflags "-s -w" -o $(BIN)/tropis-collector$(EXE) ./cmd/tropis-collector

.PHONY: build-linux
build-linux: ## Build Linux amd64 binaries into bin/linux/ (for the injection and NPD tests)
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN)/linux/tropis ./cmd/tropis
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 $(GO) build -trimpath -ldflags "-s -w" -o $(BIN)/linux/tropis-collector ./cmd/tropis-collector

.PHONY: image
image: ## Build the container image (docker; IMAGE=tropis:dev)
	docker build --build-arg VERSION=$(VERSION) -t $(IMAGE) .

##@ Check

.PHONY: test
test: ## Run the unit tests
	$(GO) test ./...

.PHONY: test-race
test-race: ## Run the unit tests with the race detector (needs cgo)
	$(GO) test -race ./...

.PHONY: vet
vet: ## go vet
	$(GO) vet ./...

.PHONY: fmt
fmt: ## Format all Go code
	gofmt -w $(PKGS)

.PHONY: fmt-check
fmt-check: ## Fail if any Go code is unformatted
	@out="$$(gofmt -l $(PKGS))"; if [ -n "$$out" ]; then echo "unformatted:"; echo "$$out"; exit 1; fi

.PHONY: generate
generate: ## Regenerate the verdict JSON schema, the CRD and the dev corpus
	$(GO) generate ./...
	$(GO) run ./hack/gen-devcorpus

.PHONY: check-generated
check-generated: generate ## Fail if generated files are out of date
	@git diff --exit-code -- docs/schema deploy/helm/tropis/crds eval/testdata || \
		{ echo "generated files are stale: run make generate and commit the result"; exit 1; }

.PHONY: npd-test
npd-test: build ## Test the NPD plugin wrapper honours NPD's exit-code protocol
	sh hack/npd-plugin/test.sh "$(CURDIR)/$(BIN)/tropis-collector$(EXE)" "$(CURDIR)/pkg/host/smart/testdata"

.PHONY: helm-lint
helm-lint: ## Lint the Helm chart (helm)
	helm lint $(CHART)

.PHONY: check
check: fmt-check vet test check-generated npd-test helm-lint ## Everything that needs no Docker: run before pushing

##@ Integration (Docker)

.PHONY: inject-test
inject-test: build-linux ## Run the fault-injection scripts against a loop device in a privileged container (docker; REQUIRE_DUST=1 fails unless dm-dust is used)
	@rc=0; for nodust in 0 1; do \
		req=0; if [ "$$nodust" = 0 ] && [ "$(REQUIRE_DUST)" = 1 ]; then req=1; fi; \
		echo "==> injection tests, TROPIS_NO_DUST=$$nodust TROPIS_REQUIRE_DUST=$$req"; \
		MSYS_NO_PATHCONV=1 docker run --rm --privileged -v /dev:/dev -v /lib/modules:/lib/modules:ro \
			-v "$(CURDIR):/repo:ro" -e TROPIS_INJECT_TEST=1 -e TROPIS_NO_DUST=$$nodust -e TROPIS_REQUIRE_DUST=$$req ubuntu:24.04 bash -c '\
				export DEBIAN_FRONTEND=noninteractive; \
				apt-get update -qq && apt-get install -y -qq dmsetup e2fsprogs util-linux jq procps smartmontools kmod >/dev/null; \
				modprobe -a dm_mod dm_flakey dm_delay dm_dust 2>/dev/null || true; \
				bash /repo/hack/inject/test.sh /repo/bin/linux/tropis /repo/bin/linux/tropis-collector' || rc=1; \
	done; exit $$rc

.PHONY: kind-e2e
kind-e2e: ## Install on a fresh kind cluster and require a verdict (docker, kind, kubectl, helm; KEEP=1 keeps the cluster)
	bash hack/kind-e2e.sh

.PHONY: smart-samples
smart-samples: ## Regenerate the captured smartctl samples (docker, privileged)
	MSYS_NO_PATHCONV=1 docker run --rm --privileged -v /dev:/dev -v /lib/modules:/lib/modules:ro \
		-v "$(CURDIR):/repo" alpine:3.20 sh /repo/hack/capture-smart-samples.sh

##@ Evaluation

.PHONY: eval-dev
eval-dev: ## Replay the synthetic dev corpus with the mock backend (measures nothing; exercises the harness)
	$(GO) run ./cmd/tropis eval --corpus eval/testdata --backend mock --out eval/results/dev $(if $(SEED),--seed $(SEED)) --quiet

.PHONY: eval
eval: ## Replay a corpus (CORPUS=eval/fixtures BACKEND=anthropic|local|mock SEED=n); refuses synthetic fixtures
	$(GO) run ./cmd/tropis eval --corpus $(CORPUS) --publishable $(if $(BACKEND),--backend $(BACKEND)) $(if $(SEED),--seed $(SEED))

##@ Misc

.PHONY: clean
clean: ## Remove build output and eval results
	rm -rf $(BIN) eval/results

.PHONY: help
help: ## List targets
	@awk 'BEGIN {FS = ":.*##"; printf "usage: make <target>\n"} \
		/^##@/ {printf "\n%s\n", substr($$0, 5)} \
		/^[a-zA-Z0-9_-]+:.*##/ {printf "  %-16s %s\n", $$1, $$2}' $(MAKEFILE_LIST)
