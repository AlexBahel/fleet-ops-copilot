{{/*
Labels added by the chart to every object it renders. `make chart-parity`
ignores exactly these keys when it compares the chart with config/.
*/}}
{{- define "fleetops.chartLabels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end -}}

{{/* Labels config/ already carries on a workload, plus the chart labels. */}}
{{- define "fleetops.workloadLabels" -}}
app.kubernetes.io/name: {{ .name }}
app.kubernetes.io/part-of: fleet-ops-copilot
{{ include "fleetops.chartLabels" .root }}
{{- end -}}

{{- define "fleetops.image" -}}
{{ printf "%s:%s" .Values.image.repository .Values.image.tag }}
{{- end -}}

{{/* The policy file text: policy.content, or the chart's copy of policies/default.yaml. */}}
{{- define "fleetops.policy" -}}
{{- if .Values.policy.content -}}
{{ .Values.policy.content }}
{{- else -}}
{{ .Files.Get "files/default-policy.yaml" }}
{{- end -}}
{{- end -}}

{{- define "fleetops.catalogue" -}}
{{- if .Values.catalogue.content -}}
{{ .Values.catalogue.content }}
{{- else -}}
{{ .Files.Get "files/site-catalogue.yaml" }}
{{- end -}}
{{- end -}}

{{/*
Environment for fleetops-agent's claude backend (templates/fleetops-agent.yaml). The key
comes from an existing Secret; the chart never holds its value.
*/}}
{{/*
fleetops-agent's model backend: its flags and environment for llm.profile.
cloud: the claude backend, the key from the existing Secret. airgap: the
llamacpp backend against llama-server, no key (D103).
*/}}
{{- define "fleetops.agentBackendArgs" -}}
{{- if eq .Values.llm.profile "airgap" -}}
- -backend=llamacpp
- -llama-url={{ .Values.llm.airgap.url }}
- -model={{ .Values.llm.airgap.model }}
{{- else -}}
- -backend=claude
{{- end -}}
{{- end -}}

{{- define "fleetops.agentBackendEnv" -}}
{{- if eq .Values.llm.profile "airgap" -}}
{{- $s := .Values.llm.airgap.sampling -}}
{{- $names := dict "temperature" "FLEETOPS_LLAMACPP_TEMPERATURE" "topP" "FLEETOPS_LLAMACPP_TOP_P" "topK" "FLEETOPS_LLAMACPP_TOP_K" "repeatPenalty" "FLEETOPS_LLAMACPP_REPEAT_PENALTY" -}}
{{- $env := list -}}
{{- range $k := list "temperature" "topP" "topK" "repeatPenalty" -}}
{{- with (get $s $k) -}}
{{- $env = append $env (dict "name" (get $names $k) "value" (toString .)) -}}
{{- end -}}
{{- end -}}
{{- toYaml $env -}}
{{- else -}}
{{- include "fleetops.claudeEnv" . -}}
{{- end -}}
{{- end -}}

{{- define "fleetops.claudeEnv" -}}
- name: ANTHROPIC_API_KEY
  valueFrom:
    secretKeyRef:
      name: {{ .Values.llm.cloud.apiKeySecretRef.name }}
      key: {{ .Values.llm.cloud.apiKeySecretRef.key }}
- name: FLEETOPS_CLAUDE_MODEL
  value: {{ .Values.llm.cloud.model | quote }}
{{- end -}}

{{/*
The OTLP/gRPC endpoint fleetops-agent and fleetops-mcp export to: the in-cluster
Collector when tracing.backend.enabled, otherwise tracing.otlpEndpoint (empty:
tracing off).
*/}}
{{- define "fleetops.otlpEndpoint" -}}
{{- if .Values.tracing.backend.enabled -}}
fleetops-otel-collector:4317
{{- else -}}
{{ .Values.tracing.otlpEndpoint }}
{{- end -}}
{{- end -}}

{{/* config.yaml of the in-cluster OpenTelemetry Collector (templates/tracing-backend.yaml). */}}
{{- define "fleetops.otelCollectorConfig" -}}
receivers:
  otlp:
    protocols:
      grpc:
        endpoint: 0.0.0.0:4317
processors:
  memory_limiter:
    check_interval: 1s
    limit_mib: 200
    spike_limit_mib: 50
  batch: {}
exporters:
  otlp:
    endpoint: fleetops-jaeger:4317
    tls:
      insecure: true
service:
  pipelines:
    traces:
      receivers: [otlp]
      processors: [memory_limiter, batch]
      exporters: [otlp]
{{- end -}}

{{/*
Render-time checks. Called from every template file so `helm template -s`
on a single file still fails on a bad configuration.
*/}}
{{- define "fleetops.validate" -}}
{{- if ne .Release.Namespace "fleetops-system" -}}
{{- fail (printf "release namespace is %q; this chart installs into fleetops-system only (the admission policies name ServiceAccounts in fleetops-system)" .Release.Namespace) -}}
{{- end -}}
{{- if not (has .Values.llm.profile (list "cloud" "airgap")) -}}
{{- fail (printf "llm.profile must be cloud or airgap, got %q" .Values.llm.profile) -}}
{{- end -}}
{{- if and (eq .Values.llm.profile "airgap") .Values.networkPolicy.claudeApiEgress.cidrs -}}
{{- fail "llm.profile=airgap with networkPolicy.claudeApiEgress.cidrs: the air-gapped profile reads no API key, and that rule would open egress to the Claude API that config/airgap's policy closes; leave the list empty" -}}
{{- end -}}
{{- if not .Values.swarmadaNamespaces -}}
{{- fail "swarmadaNamespaces must list at least one Swarmada namespace" -}}
{{- end -}}
{{- $pol := include "fleetops.policy" . | fromYaml -}}
{{- if hasKey $pol "Error" -}}
{{- fail (printf "policy file is not valid YAML: %s" $pol.Error) -}}
{{- end -}}
{{- range ($pol.namespaces | default list) -}}
{{- if not (has . $.Values.swarmadaNamespaces) -}}
{{- fail (printf "the policy file lists namespace %q, which is not in swarmadaNamespaces: fleetops-mcp and fleetops-approvals would have no RoleBinding there" .) -}}
{{- end -}}
{{- end -}}
{{- if gt (int .Values.agent.replicas) 1 -}}
{{- fail "agent.replicas must be 0 or 1: fleetops-agent keeps conversations in memory (docs/DESIGN.md D75)" -}}
{{- end -}}
{{- if and .Values.tracing.backend.enabled .Values.tracing.otlpEndpoint -}}
{{- fail "tracing.backend.enabled and tracing.otlpEndpoint are both set: the in-cluster backend is the exporter's endpoint, so leave tracing.otlpEndpoint empty (or turn the backend off to use your own collector)" -}}
{{- end -}}
{{- if and .Values.networkPolicy.enabled (not .Values.networkPolicy.apiServer.cidrs) -}}
{{- fail "networkPolicy.enabled needs networkPolicy.apiServer.cidrs (the API server endpoint and Service address); without them fleetops-mcp and fleetops-approvals cannot reach the API server" -}}
{{- end -}}
{{- end -}}
