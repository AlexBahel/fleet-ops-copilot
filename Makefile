# Fleet Ops Copilot. `make help` lists targets.

ENVTEST_K8S_VERSION ?= 1.37.0
# setup-envtest from the release-0.23 branch (needs go 1.25). The release-0.17
# build used before downloads from a storage bucket that answered 401 (2026-11-04).
SETUP_ENVTEST_VERSION ?= v0.0.0-20260305142021-f9589b9f2b9d
GO ?= go
BIN := bin

.PHONY: help build vet lint fmt test test-envtest eval-fixtures generate check-no-local all ci envtest-assets image deploy-kind test-live

help: ## List targets
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-16s %s\n", $$1, $$2}'

all: ci

ci: check-no-local build vet lint test test-envtest eval-fixtures ## Everything the CI Go job runs

build: ## Build foc, foc-mcp, foc-approvals, foc-agent into bin/
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

# Air-gapped profile (llama.cpp): load-llama-image, load-llama-model, deploy-airgap, verify-egress.
include hack/airgap.mk

# ---- Live cluster (kind, foc-* only) ----------------------------------------
# Every kubectl and kind command names the cluster explicitly; the current
# context of any kubeconfig is never used or changed.
KIND_CLUSTER ?=
IMG ?= fleet-ops-copilot:dev
ARCH ?= $(shell $(GO) env GOARCH)

image: ## Build the foc-mcp/foc-approvals/foc-agent image (binaries built on the host, see Dockerfile)
	CGO_ENABLED=0 GOOS=linux GOARCH=$(ARCH) $(GO) build -o $(BIN)/linux-$(ARCH)/ ./cmd/foc-mcp ./cmd/foc-approvals ./cmd/foc-agent
	docker build --platform linux/$(ARCH) -t $(IMG) .

deploy-kind: image ## Install CRDs, RBAC, admission policies, foc-mcp, foc-approvals and foc-agent into kind cluster KIND_CLUSTER (foc-*)
	@case "$(KIND_CLUSTER)" in foc-*) ;; *) echo "KIND_CLUSTER must be set and start with foc-"; exit 1;; esac
	kind load docker-image $(IMG) --name $(KIND_CLUSTER)
	kubectl --context kind-$(KIND_CLUSTER) apply -f config/crd/ -f config/rbac/ -f config/admission/
	kubectl --context kind-$(KIND_CLUSTER) -n foc-system create configmap fleetops-policy \
		--from-file=default.yaml=policies/default.yaml --dry-run=client -o yaml \
		| kubectl --context kind-$(KIND_CLUSTER) apply -f -
	kubectl --context kind-$(KIND_CLUSTER) apply -f config/deploy/
	kubectl --context kind-$(KIND_CLUSTER) -n foc-system rollout restart deploy/foc-mcp deploy/foc-approvals deploy/foc-agent
	kubectl --context kind-$(KIND_CLUSTER) -n foc-system rollout status deploy/foc-mcp deploy/foc-approvals --timeout=180s
	@echo "foc-agent starts once Secret fleet-ops-copilot-claude (key ANTHROPIC_API_KEY) exists in foc-system; not waited for here."

test-live: build ## Approval paths against a live cluster (hack/live-approval-path.sh sets the environment)
	$(GO) test -tags live -count=1 -v -timeout 900s ./test/live/...

# ---- Helm chart (charts/fleet-ops-copilot) -----------------------------------
# kubeconform has no schema for CustomResourceDefinition, so the CRDs are left
# out of chart-validate; chart-parity checks they are byte-identical to
# config/crd, which envtest loads into a real API server.
CHART := charts/fleet-ops-copilot
HELM ?= helm
KUBECONFORM ?= kubeconform
CHART_KUBE_VERSIONS ?= 1.30.0 1.37.0
# RFC 5737 documentation addresses, used only to render the NetworkPolicy.
CHART_NP_SET := --set networkPolicy.enabled=true --set 'networkPolicy.apiServer.cidrs={192.0.2.10/32,192.0.2.11/32}' --set 'networkPolicy.claudeApiEgress.cidrs={198.51.100.0/24}'

.PHONY: chart-lint chart-validate chart-parity chart-check

chart-lint: ## helm lint the chart (strict)
	$(HELM) lint $(CHART) --namespace foc-system --strict

chart-validate: ## Render the chart (defaults, and with NetworkPolicy) and schema-check it with kubeconform
	@command -v $(KUBECONFORM) >/dev/null || { echo "kubeconform not found (go install github.com/yannh/kubeconform/cmd/kubeconform@v0.7.0)"; exit 1; }
	@for v in $(CHART_KUBE_VERSIONS); do \
		echo "== Kubernetes $$v, defaults"; \
		$(HELM) template foc $(CHART) -n foc-system | $(KUBECONFORM) -strict -summary -kubernetes-version $$v - || exit 1; \
		echo "== Kubernetes $$v, networkPolicy.enabled"; \
		$(HELM) template foc $(CHART) -n foc-system $(CHART_NP_SET) | $(KUBECONFORM) -strict -summary -kubernetes-version $$v - || exit 1; \
	done

chart-parity: ## Check the chart's default render equals config/ (CRDs, RBAC, admission, deploy) and policies/default.yaml
	@mkdir -p $(BIN)
	$(HELM) template foc $(CHART) -n foc-system --include-crds > $(BIN)/chart-rendered.yaml
	$(GO) run ./scripts/chart-parity -rendered $(BIN)/chart-rendered.yaml

chart-check: chart-lint chart-validate chart-parity ## All chart checks
