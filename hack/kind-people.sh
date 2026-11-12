#!/usr/bin/env bash
# Copyright 2026 Alex Bahel
# Licensed under the Apache License, Version 2.0 (see LICENSE).
#
# Person identities with bearer tokens on a kind cluster (D60, TEST
# ONLY): the fictional people sam, dana and eve authenticate through the API
# server's static token file, as they do in envtest (internal/testenv
# DefaultPeople). A person with a token can be the verified requester of a
# request (D57); a person with a client certificate (hack/kind-person.sh)
# cannot. Not for any cluster that holds anything real; in production the
# people authenticate with OIDC (docs/RUNBOOK.md).
#
#   hack/kind-people.sh gen DIR
#       Writes DIR/tokens.csv (mode 600; DIR must be outside the repository; a random token per person, in the
#       kube-apiserver --token-auth-file format) unless it exists. The file
#       must exist before the cluster is created: hack/demo-up.sh does that and
#       wires it into the kind configuration (hack/kind/demo-cluster.yaml.tmpl).
#   hack/kind-people.sh bind CONTEXT NAMESPACE...
#       Binds each person's ClusterRoles in each namespace (RoleBindings,
#       applied, so it can be repeated). The ClusterRoles fleetops:approver
#       (the chart) and swarmada:* (Swarmada) must exist.
#   hack/kind-people.sh kubeconfig CONTEXT PERSON TOKENS_FILE OUT
#       Writes OUT (mode 600): a kubeconfig whose only user is PERSON with
#       their token, and a context PERSON@<cluster>. Prints the context name.
#       The token is read from TOKENS_FILE and written to OUT; it is never
#       printed and never on a command line.
#
# CONTEXT is kind-fleetops-*; KUBECONFIG must name a dedicated file, never
# ~/.kube/config (hack/lib/guard.sh; test: test/scripts/guard_test.go).
set -euo pipefail

# name|group|ClusterRoles bound in each namespace. Keep equal to
# internal/testenv DefaultPeople (test: test/scripts, TestKindPeopleMatchEnvtest).
PEOPLE=(
  "dana|fleet-ops|fleetops:approver,swarmada:fleet-manager"
  "sam|supervisors|fleetops:approver,swarmada:operator"
  "eve|auditors|fleetops:approver"
)

usage() { sed -n '/^#   hack/,/^# CONTEXT/p' "$0" | sed 's/^# \{0,1\}//' >&2; exit 2; }
die() { echo "kind-people.sh: $*" >&2; exit 1; }

guard() {
  local ctx="$1"
  [[ "$ctx" == kind-fleetops-* ]] || die "CONTEXT must be kind-fleetops-* (got '$ctx')"
  # shellcheck disable=SC2034 # read by guard_cluster (hack/lib/guard.sh)
  CLUSTER="${ctx#kind-}"
  # shellcheck source=lib/guard.sh
  source "$(dirname "$0")/lib/guard.sh"
  guard_cluster kind-people.sh
}

cmd="${1:-}"; [[ -n "$cmd" ]] || usage
shift
case "$cmd" in
  gen)
    [[ $# -eq 1 ]] || usage
    dir="$1"
    # shellcheck source=lib/guard.sh
    source "$(dirname "$0")/lib/guard.sh"
    guard_outside_repo kind-people.sh "$dir" "$(cd "$(dirname "$0")/.." && pwd)"
    chmod 700 "$dir"
    if [[ -s "$dir/tokens.csv" ]]; then
      echo "kind-people: $dir/tokens.csv exists; keeping it"
      exit 0
    fi
    (
      umask 077
      tmp="$(mktemp "$dir/tokens.XXXXXX")"
      for p in "${PEOPLE[@]}"; do
        IFS='|' read -r name group _ <<<"$p"
        # token,user,uid,"group": the kube-apiserver static token file format.
        printf '%s,%s,%s,"%s"\n' "$(openssl rand -hex 24)" "$name" "$name" "$group" >>"$tmp"
      done
      mv "$tmp" "$dir/tokens.csv"
    )
    chmod 600 "$dir/tokens.csv"
    echo "kind-people: wrote $dir/tokens.csv (${#PEOPLE[@]} people)"
    ;;
  bind)
    [[ $# -ge 2 ]] || usage
    ctx="$1"; shift
    guard "$ctx"
    for ns in "$@"; do
      for p in "${PEOPLE[@]}"; do
        IFS='|' read -r name _ roles <<<"$p"
        IFS=',' read -r -a rs <<<"$roles"
        for role in "${rs[@]}"; do
          kubectl --context "$ctx" -n "$ns" create rolebinding "person-${name}-${role//:/-}" \
            --clusterrole="$role" --user="$name" --dry-run=client -o yaml \
            | kubectl --context "$ctx" apply -f - >/dev/null
        done
      done
      echo "kind-people: bound ${#PEOPLE[@]} people in $ns"
    done
    ;;
  kubeconfig)
    [[ $# -eq 4 ]] || usage
    ctx="$1" person="$2" tokens="$3" out="$4"
    guard "$ctx"
    cluster="${ctx#kind-}"
    [[ -f "$tokens" ]] || die "$tokens is not a file"
    token="$(awk -F, -v p="$person" '$2 == p {print $1; exit}' "$tokens")"
    [[ -n "$token" ]] || die "no token for '$person' in $tokens"
    server="$(kubectl --context "$ctx" config view --minify -o jsonpath='{.clusters[0].cluster.server}')"
    ca="$(kubectl --context "$ctx" config view --minify --raw -o jsonpath='{.clusters[0].cluster.certificate-authority-data}')"
    [[ -n "$server" && -n "$ca" ]] || die "no server or CA for $ctx in KUBECONFIG"
    mkdir -p "$(dirname "$out")"
    (
      umask 077
      cat >"$out" <<YAML
apiVersion: v1
kind: Config
clusters:
  - name: $cluster
    cluster: {server: $server, certificate-authority-data: $ca}
users:
  - name: $person
    user: {token: $token}
contexts:
  - name: $person@$cluster
    context: {cluster: $cluster, user: $person}
YAML
    )
    chmod 600 "$out"
    echo "$person@$cluster"
    ;;
  *) usage ;;
esac
