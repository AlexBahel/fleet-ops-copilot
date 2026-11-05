#!/usr/bin/env bash
# Copyright 2026 Alex Bahel
# Licensed under the Apache License, Version 2.0 (see LICENSE).
#
# Creates a person (not a ServiceAccount) on a kind cluster: a client
# certificate signed by the cluster CA through the CertificateSigningRequest
# API, and a kubeconfig context for it. Used by the live tests to act as a
# named approver. Development clusters only.
#
#   hack/kind-person.sh CONTEXT USER GROUP KUBECONFIG_OUT
#
# CONTEXT is an admin context on a foc-* kind cluster. The new context in
# KUBECONFIG_OUT is named USER@<cluster>. The private key never leaves
# KUBECONFIG_OUT.
#
# A person made here authenticates with a client certificate and has no bearer
# token, so a request they file is an unverified requester (docs/DESIGN.md
# D57). For a person who can file and approve verified requests use
# hack/kind-people.sh (static tokens, R43, test only).
set -euo pipefail
ctx="$1" user="$2" group="$3" out="$4"
[[ "$ctx" == kind-foc-* ]] || { echo "CONTEXT must be kind-foc-*" >&2; exit 1; }
cluster="${ctx#kind-}"
work="$(mktemp -d)"; trap 'rm -rf "$work"' EXIT
openssl genrsa -out "$work/key.pem" 2048 2>/dev/null
openssl req -new -key "$work/key.pem" -subj "/CN=$user/O=$group" -out "$work/csr.pem"
csr="person-$(echo "$user" | tr -c 'a-z0-9-' '-' | sed 's/-*$//')"
kubectl --context "$ctx" delete csr "$csr" --ignore-not-found >/dev/null
cat <<YAML | kubectl --context "$ctx" apply -f - >/dev/null
apiVersion: certificates.k8s.io/v1
kind: CertificateSigningRequest
metadata: {name: $csr}
spec:
  request: $(base64 < "$work/csr.pem" | tr -d '\n')
  signerName: kubernetes.io/kube-apiserver-client
  expirationSeconds: 86400
  usages: [client auth]
YAML
kubectl --context "$ctx" certificate approve "$csr" >/dev/null
for _ in $(seq 30); do
  crt="$(kubectl --context "$ctx" get csr "$csr" -o jsonpath='{.status.certificate}')"
  [[ -n "$crt" ]] && break; sleep 1
done
[[ -n "$crt" ]] || { echo "certificate not issued" >&2; exit 1; }
echo "$crt" | base64 -d > "$work/crt.pem"
server="$(kubectl --context "$ctx" config view --minify -o jsonpath='{.clusters[0].cluster.server}')"
kubectl --context "$ctx" config view --minify --raw -o jsonpath='{.clusters[0].cluster.certificate-authority-data}' | base64 -d > "$work/ca.pem"
KUBECONFIG="$out" kubectl config set-cluster "$cluster" --server="$server" --certificate-authority="$work/ca.pem" --embed-certs >/dev/null
KUBECONFIG="$out" kubectl config set-credentials "$user" --client-certificate="$work/crt.pem" --client-key="$work/key.pem" --embed-certs >/dev/null
KUBECONFIG="$out" kubectl config set-context "$user@$cluster" --cluster="$cluster" --user="$user" >/dev/null
chmod 600 "$out"
echo "$user@$cluster"
