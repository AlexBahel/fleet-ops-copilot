# Fleet Ops Copilot. `make help` lists targets.

ENVTEST_K8S_VERSION ?= 1.37.0
# setup-envtest from the release-0.23 branch (needs go 1.25). The release-0.17
# build used before downloads from a storage bucket that answered 401 (2026-11-04).
SETUP_ENVTEST_VERSION ?= v0.0.0-20260305142021-f9589b9f2b9d
GO ?= go
BIN := bin

.PHONY: help build vet lint shellcheck fmt test test-envtest eval-fixtures eval-baseline generate check-no-local all ci envtest-assets image deploy-kind test-live-go demo demo-down test-live baseline-lowered

help: ## List targets
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-16s %s\n", $$1, $$2}'

all: ci

ci: check-no-local build vet lint shellcheck test test-envtest eval-fixtures ## Everything the CI Go job runs, and the hygiene job's shellcheck

build: ## Build fleetops, fleetops-mcp, fleetops-approvals, fleetops-agent into bin/
	$(GO) build -o $(BIN)/ ./cmd/...

vet: ## go vet (including envtest-tagged files)
	$(GO) vet ./...
	$(GO) vet -tags envtest ./test/...
	$(GO) vet -tags image ./test/image

lint: ## golangci-lint
	golangci-lint run ./...

shellcheck: ## ShellCheck 0.11.0 (pinned, downloaded once into bin/) on hack/, hack/lib/ and scripts/; the CI hygiene job runs the same target
	hack/shellcheck.sh

fmt: ## gofmt
	gofmt -w api cmd internal test evals

test: ## Unit tests
	$(GO) test -count=1 ./...

envtest-assets:
	@command -v setup-envtest >/dev/null || $(GO) install sigs.k8s.io/controller-runtime/tools/setup-envtest@$(SETUP_ENVTEST_VERSION)

test-envtest: envtest-assets ## API-server tests: RBAC, admission policies, approval path
	KUBEBUILDER_ASSETS="$$(setup-envtest use $(ENVTEST_K8S_VERSION) -p path)" \
		$(GO) test -tags envtest -count=1 -timeout 600s ./test/envtest/...

eval-fixtures: envtest-assets ## Fixture evaluations with the scripted backend, held to evals/baseline.json; writes evals/results/fixtures/results.json
	KUBEBUILDER_ASSETS="$$(setup-envtest use $(ENVTEST_K8S_VERSION) -p path)" \
		$(GO) run -tags envtest ./evals/cmd/fleetops-eval -scenarios evals/scenarios -runs 3 -out evals/results/fixtures/results.json -baseline evals/baseline.json

eval-baseline: envtest-assets ## Rewrite evals/baseline.json from a fully passing scripted run (commit it with the change that moved the counts)
	KUBEBUILDER_ASSETS="$$(setup-envtest use $(ENVTEST_K8S_VERSION) -p path)" \
		$(GO) run -tags envtest ./evals/cmd/fleetops-eval -scenarios evals/scenarios -runs 3 -out evals/results/tmp/results.json -baseline evals/baseline.json -update-baseline

generate: ## Regenerate deepcopy and CRDs from api/
	controller-gen object:headerFile=hack/boilerplate.go.txt paths=./api/...
	controller-gen crd paths=./api/... output:crd:dir=config/crd

check-no-local: ## Fail if anything under local/ or go.work is tracked
	@tracked="$$(git ls-files -- local go.work go.work.sum)"; if [ -n "$$tracked" ]; then echo "tracked but must stay local: $$tracked"; exit 1; fi

# Air-gapped profile (llama.cpp): load-llama-image, load-llama-model, deploy-airgap, verify-egress.
include hack/airgap.mk

# ---- Live cluster (kind, fleetops-* only) ----------------------------------------
# Every kubectl and kind command names the cluster explicitly; the current
# context of any kubeconfig is never used or changed.
KIND_CLUSTER ?=
IMG ?= fleet-ops-copilot:dev
ARCH ?= $(shell $(GO) env GOARCH)

test-image: ## Build the binaries under umask 077 and the image, then run each binary in it as user 65532 (R60; needs Docker)
	$(GO) test -tags image -count=1 -v ./test/image

image: ## Build the fleetops-mcp/fleetops-approvals/fleetops-agent image (binaries built on the host, see Dockerfile)
	CGO_ENABLED=0 GOOS=linux GOARCH=$(ARCH) $(GO) build -o $(BIN)/linux-$(ARCH)/ ./cmd/fleetops-mcp ./cmd/fleetops-approvals ./cmd/fleetops-agent
	docker build --platform linux/$(ARCH) -t $(IMG) .

deploy-kind: image ## Install CRDs, RBAC, admission policies, fleetops-mcp, fleetops-approvals and fleetops-agent into kind cluster KIND_CLUSTER (fleetops-*)
	@case "$(KIND_CLUSTER)" in fleetops-*) ;; *) echo "KIND_CLUSTER must be set and start with fleetops-"; exit 1;; esac
	kind load docker-image $(IMG) --name $(KIND_CLUSTER)
	kubectl --context kind-$(KIND_CLUSTER) apply -f config/crd/ -f config/rbac/ -f config/admission/
	kubectl --context kind-$(KIND_CLUSTER) -n fleetops-system create configmap fleetops-policy \
		--from-file=default.yaml=policies/default.yaml --dry-run=client -o yaml \
		| kubectl --context kind-$(KIND_CLUSTER) apply -f -
	kubectl --context kind-$(KIND_CLUSTER) -n fleetops-system create configmap fleetops-catalogue \
		--from-file=site.yaml=catalogue/warehouse-a.yaml --dry-run=client -o yaml \
		| kubectl --context kind-$(KIND_CLUSTER) apply -f -
	kubectl --context kind-$(KIND_CLUSTER) apply -f config/deploy/
	kubectl --context kind-$(KIND_CLUSTER) -n fleetops-system rollout restart deploy/fleetops-mcp deploy/fleetops-approvals deploy/fleetops-agent
	kubectl --context kind-$(KIND_CLUSTER) -n fleetops-system rollout status deploy/fleetops-mcp deploy/fleetops-approvals --timeout=180s
	@echo "fleetops-agent starts once Secret fleet-ops-copilot-claude (key ANTHROPIC_API_KEY) exists in fleetops-system; not waited for here."

test-live-go: build ## Go live tests of the approval paths (hack/live-approval-path.sh sets their environment; make test-live runs them on the demo stack)
	$(GO) test -tags live -count=1 -v -timeout 900s ./test/live/...

# ---- demo: an empty machine to a running stack on kind ----------------------
# Needs Docker, kind, kubectl, helm, jq, openssl, python3 (for Swarmada's live
# quickstart). Every command names the dedicated kubeconfig below and the
# context kind-$(FLEETOPS_CLUSTER); ~/.kube/config is never read or written
# (hack/lib/guard.sh). DEMO_* variables (hack/demo-up.sh header) pass through
# from the environment: DEMO_DRY_RUN=1, DEMO_TRACING=1, DEMO_ANTHROPIC_KEY_FILE.
FLEETOPS_CLUSTER ?= fleetops-demo
# Outside the repository on purpose: it holds the people's bearer tokens (the scripts refuse a directory inside it).
DEMO_DIR ?= $(HOME)/.local/state/fleet-ops-copilot/demo
FLEETOPS_KUBECONFIG ?= $(DEMO_DIR)/kubeconfig
# Compared with the base revision: BASE=<revision> (default: merge base with origin/main).
BASE ?=

demo: ## kind cluster fleetops-demo with person tokens, Swarmada quickstart at the go.mod pin, its webhooks, the chart, health checks; prints the elapsed time (DEMO_DRY_RUN=1 prints the plan)
	CLUSTER=$(FLEETOPS_CLUSTER) KUBECONFIG=$(FLEETOPS_KUBECONFIG) DEMO_DIR=$(DEMO_DIR) hack/demo-up.sh

demo-down: ## Remove the demo cluster, its kubeconfig and the people's tokens
	CLUSTER=$(FLEETOPS_CLUSTER) KUBECONFIG=$(FLEETOPS_KUBECONFIG) DEMO_DIR=$(DEMO_DIR) hack/demo-down.sh

test-live: build ## Live approval paths on the demo stack, requests filed by sam's verified token (phase webhooks)
	CLUSTER=$(FLEETOPS_CLUSTER) KUBECONFIG=$(FLEETOPS_KUBECONFIG) PHASE=webhooks WORK=$(DEMO_DIR)/live \
		TOKENS_FILE=$(DEMO_DIR)/people/tokens.csv EVIDENCE_DIR=$(DEMO_DIR)/live hack/live-approval-path.sh

baseline-lowered: ## Print a warning for every category evals/baseline.json lowers against BASE (what CI annotates)
	$(GO) run ./scripts/baseline-lowered $(if $(BASE),-base $(BASE))

# ---- Helm chart (charts/fleet-ops-copilot) -----------------------------------
# kubeconform has no schema for CustomResourceDefinition, so the CRDs are left
# out of chart-validate; chart-parity checks they are byte-identical to
# config/crd, which envtest loads into a real API server.
CHART := charts/fleet-ops-copilot
HELM ?= helm
KUBECONFORM ?= kubeconform
CHART_KUBE_VERSIONS ?= 1.30.0 1.37.0
# The optional in-cluster trace backend (OpenTelemetry Collector + Jaeger), off by default.
CHART_TRACING_SET := --set tracing.backend.enabled=true
# RFC 5737 documentation addresses, used only to render the NetworkPolicy.
CHART_NP_SET := --set networkPolicy.enabled=true --set 'networkPolicy.apiServer.cidrs={192.0.2.10/32,192.0.2.11/32}' --set 'networkPolicy.claudeApiEgress.cidrs={198.51.100.0/24}'

.PHONY: chart-lint chart-validate chart-parity chart-check

chart-lint: ## helm lint the chart (strict): defaults, tracing backend with NetworkPolicy, tracing backend alone
	$(HELM) lint $(CHART) --namespace fleetops-system --strict
	$(HELM) lint $(CHART) --namespace fleetops-system --strict $(CHART_TRACING_SET) $(CHART_NP_SET)
	$(HELM) lint $(CHART) --namespace fleetops-system --strict $(CHART_TRACING_SET)

chart-validate: ## Render the chart (defaults, with NetworkPolicy, with the tracing backend) and schema-check it with kubeconform
	@command -v $(KUBECONFORM) >/dev/null || { echo "kubeconform not found (go install github.com/yannh/kubeconform/cmd/kubeconform@v0.7.0)"; exit 1; }
	@for v in $(CHART_KUBE_VERSIONS); do \
		echo "== Kubernetes $$v, defaults"; \
		$(HELM) template fleetops $(CHART) -n fleetops-system | $(KUBECONFORM) -strict -summary -kubernetes-version $$v - || exit 1; \
		echo "== Kubernetes $$v, networkPolicy.enabled"; \
		$(HELM) template fleetops $(CHART) -n fleetops-system $(CHART_NP_SET) | $(KUBECONFORM) -strict -summary -kubernetes-version $$v - || exit 1; \
		echo "== Kubernetes $$v, tracing.backend.enabled without networkPolicy (as beside config/airgap)"; \
		$(HELM) template fleetops $(CHART) -n fleetops-system $(CHART_TRACING_SET) | $(KUBECONFORM) -strict -summary -kubernetes-version $$v - || exit 1; \
		echo "== Kubernetes $$v, tracing.backend.enabled with networkPolicy.enabled"; \
		$(HELM) template fleetops $(CHART) -n fleetops-system $(CHART_TRACING_SET) $(CHART_NP_SET) | $(KUBECONFORM) -strict -summary -kubernetes-version $$v - || exit 1; \
	done

chart-parity: ## Check the chart's default render equals config/ (CRDs, RBAC, admission, deploy) and policies/default.yaml
	@mkdir -p $(BIN)
	$(HELM) template fleetops $(CHART) -n fleetops-system --include-crds > $(BIN)/chart-rendered.yaml
	$(GO) run ./scripts/chart-parity -rendered $(BIN)/chart-rendered.yaml

chart-check: chart-lint chart-validate chart-parity ## All chart checks
