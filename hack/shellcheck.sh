#!/usr/bin/env bash
# Copyright 2026 Alex Bahel
# Licensed under the Apache License, Version 2.0 (see LICENSE).
#
# hack/shellcheck.sh — runs one pinned ShellCheck release on the repository's
# shell scripts. The CI hygiene job and `make ci` both run it through the
# make target of the same name, so a finding does not depend on the version
# a machine has installed. The release archive is downloaded once into bin/ and checked
# against its SHA-256 (the digests GitHub lists for the v0.11.0 assets).
set -euo pipefail

version=0.11.0
cd "$(dirname "$0")/.."
bin="bin/shellcheck-v${version}/shellcheck"

sha256() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | cut -d' ' -f1
  else
    shasum -a 256 "$1" | cut -d' ' -f1
  fi
}

if [ ! -x "$bin" ]; then
  case "$(uname -s).$(uname -m)" in
  Darwin.arm64) platform=darwin.aarch64 sum=56affdd8de5527894dca6dc3d7e0a99a873b0f004d7aabc30ae407d3f48b0a79 ;;
  Darwin.x86_64) platform=darwin.x86_64 sum=3c89db4edcab7cf1c27bff178882e0f6f27f7afdf54e859fa041fca10febe4c6 ;;
  Linux.aarch64) platform=linux.aarch64 sum=12b331c1d2db6b9eb13cfca64306b1b157a86eb69db83023e261eaa7e7c14588 ;;
  Linux.x86_64) platform=linux.x86_64 sum=8c3be12b05d5c177a04c29e3c78ce89ac86f1595681cab149b65b97c4e227198 ;;
  *)
    echo "no pinned ShellCheck build for $(uname -s) $(uname -m)" >&2
    exit 1
    ;;
  esac
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' EXIT
  archive="$tmp/shellcheck.tar.xz"
  curl -sSfL -o "$archive" "https://github.com/koalaman/shellcheck/releases/download/v${version}/shellcheck-v${version}.${platform}.tar.xz"
  got="$(sha256 "$archive")"
  if [ "$got" != "$sum" ]; then
    echo "shellcheck-v${version}.${platform}.tar.xz: sha256 $got, want $sum" >&2
    exit 1
  fi
  tar -xJf "$archive" -C "$tmp"
  mkdir -p "$(dirname "$bin")"
  mv "$tmp/shellcheck-v${version}/shellcheck" "$bin"
fi

if ! "$bin" --version | grep -qx "version: ${version}"; then
  echo "$bin is not ShellCheck ${version}" >&2
  exit 1
fi
echo "ShellCheck ${version}: $bin"
exec "$bin" -x hack/*.sh hack/lib/*.sh scripts/*.sh
