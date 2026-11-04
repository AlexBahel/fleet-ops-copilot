# Copyright 2026 Alex Bahel
# Licensed under the Apache License, Version 2.0 (see LICENSE).
#
# Runtime image for foc-mcp and foc-approvals. The binaries are built on the
# host (make image) because, until the Swarmada module is pinned in go.mod,
# it is resolved through a local workspace that a container build cannot see
# (docs/DESIGN.md D2). The :debug-nonroot base adds a busybox shell so the
# audit file can be read with `kubectl exec`.
FROM gcr.io/distroless/static:debug-nonroot
ARG TARGETARCH
COPY bin/linux-${TARGETARCH}/foc-mcp bin/linux-${TARGETARCH}/foc-approvals /usr/local/bin/
COPY policies/default.yaml /etc/fleetops/policies/default.yaml
USER 65532:65532
