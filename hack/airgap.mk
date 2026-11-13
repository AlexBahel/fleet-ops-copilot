# Copyright 2026 Alex Bahel
# Licensed under the Apache License, Version 2.0 (see LICENSE).
#
# Air-gapped profile targets (docs/DESIGN.md §2, §3.5, §3.7), included by the
# Makefile. Same rules as the live-cluster targets there: only kind clusters
# named fleetops-*, and every kubectl names its context explicitly.
#
# Single-node kind clusters only: the model sits in the node's /models
# directory (config/airgap/llama-storage-hostpath.yaml).

.PHONY: load-llama-image load-llama-model deploy-airgap verify-egress verify-egress-baseline validate-airgap

LLAMA_REPO   ?= ghcr.io/ggml-org/llama.cpp
LLAMA_TAG    ?= server-b12004
# Index digest of that tag (linux/amd64, linux/arm64, linux/s390x), read from the registry.
LLAMA_DIGEST ?= sha256:b32890309c1a33a534a01b0b461e953dc2c5a6bac20213abcf48e22847a19c85
LLAMA_IMG    ?= $(LLAMA_REPO):$(LLAMA_TAG)

# Directory with the GGUF shards, and a sha256sum-format file naming them.
MODEL_DIR    ?=
MODEL_SUMS   ?=
# The model files make demo fetches (hack/fetch-model.sh) and checks against
# config/airgap/model-SHA256SUMS: the files of the v6 record runs.
MODEL_FILES  ?= qwen2.5-7b-instruct-q4_k_m-00001-of-00002.gguf qwen2.5-7b-instruct-q4_k_m-00002-of-00002.gguf

KIND_NODE     = $(KIND_CLUSTER)-control-plane
AIRGAP_NS     = fleetops-system
KCTL          = kubectl --context kind-$(KIND_CLUSTER)

define require_fleetops_cluster
	@case "$(KIND_CLUSTER)" in fleetops-*) ;; *) echo "KIND_CLUSTER must be set and start with fleetops-"; exit 1;; esac
endef

validate-airgap: ## Schema-check config/airgap with kubeconform (KUBE_VERSION, default 1.34.0)
	@command -v kubeconform >/dev/null || { echo "kubeconform not found"; exit 1; }
	kubeconform -strict -summary -kubernetes-version $(or $(KUBE_VERSION),1.34.0) config/airgap/

load-llama-image: ## Put the pinned llama.cpp server image into kind cluster KIND_CLUSTER (fleetops-*); pulls only if the host lacks it
	$(require_fleetops_cluster)
	@mkdir -p $(BIN)/airgap
	docker image inspect $(LLAMA_REPO)@$(LLAMA_DIGEST) >/dev/null 2>&1 || docker pull --platform linux/$(ARCH) $(LLAMA_REPO)@$(LLAMA_DIGEST)
	docker tag $(LLAMA_REPO)@$(LLAMA_DIGEST) $(LLAMA_IMG)
	docker save --platform linux/$(ARCH) -o $(BIN)/airgap/llama-server-$(ARCH).tar $(LLAMA_IMG)
	kind load image-archive $(BIN)/airgap/llama-server-$(ARCH).tar --name $(KIND_CLUSTER)
	@echo "image in the node's containerd store:"
	docker exec $(KIND_NODE) crictl images | grep -E 'IMAGE|llama.cpp'

load-llama-model: ## Copy the GGUF shards from MODEL_DIR into /models on the kind node, verified against MODEL_SUMS
	$(require_fleetops_cluster)
	@test -n "$(MODEL_DIR)" -a -n "$(MODEL_SUMS)" || { echo "set MODEL_DIR=<dir with the .gguf files> and MODEL_SUMS=<sha256sum file>"; exit 1; }
	cd "$(MODEL_DIR)" && shasum -a 256 -c "$(abspath $(MODEL_SUMS))"
	docker exec $(KIND_NODE) mkdir -p /models
	for f in $(MODEL_FILES); do docker cp "$(MODEL_DIR)/$$f" $(KIND_NODE):/models/$$f || exit 1; done
	docker exec $(KIND_NODE) chmod -R a+rX /models
	docker exec -i $(KIND_NODE) sh -c 'cd /models && sha256sum -c -' < "$(MODEL_SUMS)"

deploy-airgap: ## Apply llama-server and the egress NetworkPolicies to kind cluster KIND_CLUSTER (fleetops-*), plus the OTLP rule if the chart's trace backend is installed; restarts fleetops-mcp/fleetops-approvals, and fleetops-agent if deployed
	$(require_fleetops_cluster)
	@mkdir -p $(BIN)/airgap
	$(KCTL) apply -f config/airgap/llama-storage-hostpath.yaml -f config/airgap/llama-server.yaml
	$(KCTL) -n $(AIRGAP_NS) rollout status deploy/llama-server --timeout=900s
	@ep="$$($(KCTL) -n default get endpointslices -l kubernetes.io/service-name=kubernetes -o jsonpath='{.items[0].endpoints[0].addresses[0]}')"; \
	svc="$$($(KCTL) -n default get svc kubernetes -o jsonpath='{.spec.clusterIP}')"; \
	if [ -z "$$ep" ] || [ -z "$$svc" ]; then echo "could not read the API server addresses"; exit 1; fi; \
	echo "API server endpoint $$ep, Service $$svc"; \
	sed -e "s|192\.0\.2\.10/32|$$ep/32|" -e "s|192\.0\.2\.11/32|$$svc/32|" config/airgap/networkpolicy.yaml > $(BIN)/airgap/networkpolicy.rendered.yaml; \
	if grep -q 'cidr: 192\.0\.2\.' $(BIN)/airgap/networkpolicy.rendered.yaml; then echo "placeholder address left in the rendered policy"; exit 1; fi; \
	$(KCTL) apply -f $(BIN)/airgap/networkpolicy.rendered.yaml
	@# With the chart's trace backend: fleetops-mcp/fleetops-agent -> Collector OTLP (D69).
	@if $(KCTL) -n $(AIRGAP_NS) get deploy/fleetops-otel-collector >/dev/null 2>&1; then \
		$(KCTL) apply -f config/airgap/networkpolicy-tracing.yaml; \
	else echo "no fleetops-otel-collector in $(AIRGAP_NS) (tracing.backend.enabled off): networkpolicy-tracing.yaml not applied"; fi
	@# Established API connections can outlive a wrong address; new pods cannot.
	@if $(KCTL) -n $(AIRGAP_NS) get deploy/fleetops-mcp deploy/fleetops-approvals >/dev/null 2>&1; then \
		$(KCTL) -n $(AIRGAP_NS) rollout restart deploy/fleetops-mcp deploy/fleetops-approvals && \
		$(KCTL) -n $(AIRGAP_NS) rollout status deploy/fleetops-mcp deploy/fleetops-approvals --timeout=180s; \
	else echo "fleetops-mcp/fleetops-approvals not deployed (install the chart first): API access under the policy not exercised"; fi
	@if $(KCTL) -n $(AIRGAP_NS) get deploy/fleetops-agent >/dev/null 2>&1; then \
		$(KCTL) -n $(AIRGAP_NS) rollout restart deploy/fleetops-agent && \
		$(KCTL) -n $(AIRGAP_NS) rollout status deploy/fleetops-agent --timeout=180s; \
	else echo "fleetops-agent not deployed"; fi

verify-egress: ## Egress probe Job in fleetops-system: records egress at pod start, waits until the policy is in force, then passes only if every external target is blocked and the in-namespace control works
	$(require_fleetops_cluster)
	$(KCTL) -n $(AIRGAP_NS) delete job fleetops-egress-probe --ignore-not-found --wait
	$(KCTL) -n $(AIRGAP_NS) apply -f config/airgap/egress-probe.yaml
	@ok=0; for i in $$(seq 1 60); do \
		s="$$($(KCTL) -n $(AIRGAP_NS) get job fleetops-egress-probe -o jsonpath='{.status.succeeded}')"; \
		f="$$($(KCTL) -n $(AIRGAP_NS) get job fleetops-egress-probe -o jsonpath='{.status.failed}')"; \
		if [ "$$s" = 1 ]; then ok=1; break; fi; \
		if [ -n "$$f" ] && [ "$$f" != 0 ]; then break; fi; sleep 3; done; \
	$(KCTL) -n $(AIRGAP_NS) logs job/fleetops-egress-probe; \
	if [ $$ok = 1 ]; then echo "verify-egress: PASS (after the policy was in force: external blocked, control reachable; the at-start line is not counted)"; else echo "verify-egress: FAIL"; exit 1; fi

verify-egress-baseline: ## Same probe in a namespace WITHOUT the policy: must report open egress (shows the probe detects it from this network)
	$(require_fleetops_cluster)
	$(KCTL) create namespace fleetops-egress-baseline --dry-run=client -o yaml | $(KCTL) apply -f -
	$(KCTL) -n fleetops-egress-baseline delete job fleetops-egress-probe --ignore-not-found --wait
	sed -e 's|value: "http://llama-server:8080/health"|value: ""|' -e 's|value: "https://kubernetes.default.svc:443/healthz"|value: ""|' config/airgap/egress-probe.yaml | $(KCTL) -n fleetops-egress-baseline apply -f -
	@# No control and no policy sentinel: no policy here, so no wait (D63).
	@res=unfinished; for i in $$(seq 1 60); do \
		s="$$($(KCTL) -n fleetops-egress-baseline get job fleetops-egress-probe -o jsonpath='{.status.succeeded}')"; \
		f="$$($(KCTL) -n fleetops-egress-baseline get job fleetops-egress-probe -o jsonpath='{.status.failed}')"; \
		if [ "$$s" = 1 ]; then res=blocked; break; fi; \
		if [ -n "$$f" ] && [ "$$f" != 0 ]; then res=open; break; fi; sleep 3; done; \
	$(KCTL) -n fleetops-egress-baseline logs job/fleetops-egress-probe; \
	$(KCTL) delete namespace fleetops-egress-baseline --wait=false; \
	case $$res in \
		open) echo "baseline: egress is open without the policy, as expected; the probe can detect it";; \
		blocked) echo "baseline INCONCLUSIVE: egress is also blocked without the policy (no route from this host, or the CNI blocks it anyway)"; exit 1;; \
		*) echo "baseline did not finish"; exit 1;; \
	esac
