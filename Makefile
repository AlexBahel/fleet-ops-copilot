# Fleet Ops Copilot. `make help` lists targets.

ENVTEST_K8S_VERSION ?= 1.37.0
SETUP_ENVTEST_VERSION ?= release-0.17
GO ?= go
BIN := bin

.PHONY: help build vet lint fmt test test-envtest eval-fixtures generate check-no-local all ci envtest-assets

help: ## List targets
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-16s %s\n", $$1, $$2}'

all: ci

ci: check-no-local build vet lint test test-envtest eval-fixtures ## Everything the CI Go job runs (run locally until the Swarmada pin)

build: ## Build foc, foc-mcp, foc-approvals into bin/
	$(GO) build -o $(BIN)/ ./cmd/...

vet: ## go vet (including envtest-tagged files)
	$(GO) vet ./...
	$(GO) vet -tags envtest ./test/...

lint: ## golangci-lint
	golangci-lint run ./...

fmt: ## gofmt
	gofmt -w api cmd internal test evals

test: ## Unit tests
	$(GO) test -count=1 ./...

envtest-assets:
	@command -v setup-envtest >/dev/null || $(GO) install sigs.k8s.io/controller-runtime/tools/setup-envtest@$(SETUP_ENVTEST_VERSION)

test-envtest: envtest-assets ## API-server tests: RBAC, admission policies, approval path
	KUBEBUILDER_ASSETS="$$(setup-envtest use $(ENVTEST_K8S_VERSION) -p path)" \
		$(GO) test -tags envtest -count=1 -timeout 600s ./test/envtest/...

eval-fixtures: envtest-assets ## Fixture evaluations with the scripted backend; writes evals/results/fixtures/results.json
	KUBEBUILDER_ASSETS="$$(setup-envtest use $(ENVTEST_K8S_VERSION) -p path)" \
		$(GO) run -tags envtest ./evals/cmd/foc-eval -scenarios evals/scenarios -runs 3 -out evals/results/fixtures/results.json

generate: ## Regenerate deepcopy and CRDs from api/
	controller-gen object:headerFile=hack/boilerplate.go.txt paths=./api/...
	controller-gen crd paths=./api/... output:crd:dir=config/crd

check-no-local: ## Fail if anything under local/ or go.work is tracked
	@tracked="$$(git ls-files -- local go.work go.work.sum)"; if [ -n "$$tracked" ]; then echo "tracked but must stay local: $$tracked"; exit 1; fi
