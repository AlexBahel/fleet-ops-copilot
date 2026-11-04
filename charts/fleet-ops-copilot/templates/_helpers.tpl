{{/*
Labels added by the chart to every object it renders. `make chart-parity`
ignores exactly these keys when it compares the chart with config/.
*/}}
{{- define "foc.chartLabels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end -}}

{{/* Labels config/ already carries on a workload, plus the chart labels. */}}
{{- define "foc.workloadLabels" -}}
app.kubernetes.io/name: {{ .name }}
app.kubernetes.io/part-of: fleet-ops-copilot
{{ include "foc.chartLabels" .root }}
{{- end -}}

{{- define "foc.image" -}}
{{ printf "%s:%s" .Values.image.repository .Values.image.tag }}
{{- end -}}

{{/* The policy file text: policy.content, or the chart's copy of policies/default.yaml. */}}
{{- define "foc.policy" -}}
{{- if .Values.policy.content -}}
{{ .Values.policy.content }}
{{- else -}}
{{ .Files.Get "files/default-policy.yaml" }}
{{- end -}}
{{- end -}}

{{/*
Environment for the claude backend, for the foc-agent Deployment once
cmd/foc-agent exists. Not used by any template on main. The key comes from an
existing Secret; the chart never holds its value.
*/}}
{{- define "foc.claudeEnv" -}}
- name: ANTHROPIC_API_KEY
  valueFrom:
    secretKeyRef:
      name: {{ .Values.llm.cloud.apiKeySecretRef.name }}
      key: {{ .Values.llm.cloud.apiKeySecretRef.key }}
- name: FOC_CLAUDE_MODEL
  value: {{ .Values.llm.cloud.model | quote }}
{{- end -}}

{{/*
Render-time checks. Called from every template file so `helm template -s`
on a single file still fails on a bad configuration.
*/}}
{{- define "foc.validate" -}}
{{- if ne .Release.Namespace "foc-system" -}}
{{- fail (printf "release namespace is %q; this chart installs into foc-system only (the admission policies name ServiceAccounts in foc-system)" .Release.Namespace) -}}
{{- end -}}
{{- if eq .Values.llm.profile "airgap" -}}
{{- fail "llm.profile=airgap is not templated yet: the air-gapped profile (llama-server, model storage, egress policy) is installed beside the chart from config/airgap with make deploy-airgap. Install with llm.profile=cloud; see docs/RUNBOOK.md, \"Switch between cloud and air-gapped\"." -}}
{{- end -}}
{{- if ne .Values.llm.profile "cloud" -}}
{{- fail (printf "llm.profile must be cloud or airgap, got %q" .Values.llm.profile) -}}
{{- end -}}
{{- if not .Values.swarmadaNamespaces -}}
{{- fail "swarmadaNamespaces must list at least one Swarmada namespace" -}}
{{- end -}}
{{- $pol := include "foc.policy" . | fromYaml -}}
{{- if hasKey $pol "Error" -}}
{{- fail (printf "policy file is not valid YAML: %s" $pol.Error) -}}
{{- end -}}
{{- range ($pol.namespaces | default list) -}}
{{- if not (has . $.Values.swarmadaNamespaces) -}}
{{- fail (printf "the policy file lists namespace %q, which is not in swarmadaNamespaces: foc-mcp and foc-approvals would have no RoleBinding there" .) -}}
{{- end -}}
{{- end -}}
{{- if and .Values.networkPolicy.enabled (not .Values.networkPolicy.apiServer.cidrs) -}}
{{- fail "networkPolicy.enabled needs networkPolicy.apiServer.cidrs (the API server endpoint and Service address); without them foc-mcp and foc-approvals cannot reach the API server" -}}
{{- end -}}
{{- end -}}
