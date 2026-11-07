# Copyright 2026 Alex Bahel
# Licensed under the Apache License, Version 2.0 (see LICENSE).
# shellcheck shell=bash
#
# Sourced by the hack/ scripts that act on a live kind cluster
# (live-scenarios.sh, capture-live-fixtures.sh, live-approval-path.sh).
#
# foc_guard <script name> refuses (exit 1) unless CLUSTER matches
# ^foc-[a-z0-9-]+$ and KUBECONFIG names a dedicated file: the Swarmada
# quickstart runs `kubectl config use-context`, and kind writes contexts,
# into the file KUBECONFIG names, so KUBECONFIG must be set and no entry in
# it may resolve to ~/.kube/config (same path, symlink or hard link).
# Test: test/scripts/guard_test.go.

foc_guard() {
  local me="$1" default_kubeconfig default_physical k physical entries=0
  local -a kubeconfig_entries
  refuse() { echo "$me: refused: $*" >&2; exit 1; }
  [[ "${CLUSTER:-}" =~ ^foc-[a-z0-9-]+$ ]] || refuse "CLUSTER must match ^foc-[a-z0-9-]+\$ (got '${CLUSTER:-}')"
  [[ -n "${KUBECONFIG:-}" ]] || refuse "KUBECONFIG must be set to a dedicated kubeconfig file, not ~/.kube/config"
  default_kubeconfig="${HOME:?}/.kube/config"
  default_physical="$(cd "$HOME/.kube" 2>/dev/null && pwd -P)/config" || default_physical="$default_kubeconfig"
  IFS=: read -r -a kubeconfig_entries <<<"$KUBECONFIG"
  for k in "${kubeconfig_entries[@]}"; do
    [[ -n "$k" ]] || continue
    entries=$((entries + 1))
    k="${k/#\~/$HOME}"
    [[ "$k" == /* ]] || k="$PWD/$k"
    physical="$(cd "$(dirname "$k")" 2>/dev/null && pwd -P)/$(basename "$k")" || physical="$k"
    # -ef: same device and inode, so symlinks and hard links to the file are caught;
    # the path comparisons catch the file when it does not exist yet.
    if [[ "$k" -ef "$default_kubeconfig" || "$k" == "$default_kubeconfig" \
      || "$physical" == "$default_kubeconfig" || "$physical" == "$default_physical" ]]; then
      refuse "KUBECONFIG entry '$k' resolves to ~/.kube/config; use a dedicated file (e.g. kind get kubeconfig --name \"$CLUSTER\" > file)"
    fi
  done
  ((entries > 0)) || refuse "KUBECONFIG names no file"
}

# foc_outside_repo <script name> <directory> <repo root> refuses (exit 1) when
# the directory is the repository or inside it. foc-up keeps the people's
# bearer tokens, the kubeconfigs and a copy of Swarmada there; a clone has no
# .git/info/exclude of the author's, so such files must not sit in the work
# tree where `git add -A` could take them. The directory is created if needed.
foc_outside_repo() {
  local me="$1" d="$2" repo="$3" probe rest="" real_d real_repo
  # Resolve through the nearest existing ancestor first, so that nothing is
  # created inside the repository before the check.
  probe="$d"
  while [[ ! -d "$probe" ]]; do rest="/$(basename "$probe")$rest"; probe="$(dirname "$probe")"; done
  real_d="$(cd "$probe" && pwd -P)$rest"
  real_repo="$(cd "$repo" && pwd -P)"
  if [[ "$real_d" == "$real_repo" || "$real_d" == "$real_repo"/* ]]; then
    echo "$me: refused: DEMO_DIR '$real_d' is inside the repository '$real_repo'; it holds bearer tokens, use a directory outside it (default $HOME/.local/state/fleet-ops-copilot/foc-up)" >&2
    exit 1
  fi
  mkdir -p "$d"
}
