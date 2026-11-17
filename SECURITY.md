# Security policy

## Reporting a vulnerability

Report a vulnerability privately through GitHub: on this repository's **Security** tab, choose
**Report a vulnerability** (GitHub's private vulnerability reporting). The report is visible only to
the maintainer and to you. Do not open a public issue, pull request or discussion for it.

Include what you can of:

- the component (`fleetops-agent`, `fleetops-mcp`, `fleetops-approvals`, the `fleetops` CLI, the Helm
  chart, the manifests in `config/`, or the air-gapped profile) and the commit you tested;
- the steps or input that show the problem, and what you expected instead;
- what an attacker gains: for example a write to Swarmada objects without a named person's approval,
  an approval by the requester or by a ServiceAccount, a requester identity that is not verified,
  egress from the air-gapped profile, or a secret in a log, trace, audit line or Event.

The report is acknowledged in the advisory thread. A confirmed vulnerability is fixed on `main` and
published as a GitHub security advisory, crediting the reporter unless they ask not to be named.

## Supported versions

There are no releases yet. Only the latest commit on `main` is supported.

## Scope

In scope: the code, chart and manifests in this repository, and the guarantees in
[`docs/DESIGN.md`](docs/DESIGN.md#guarantees-and-where-each-is-enforced) (the model holds no write permission, every change needs a named
person's approval, the forbidden operations stay refused).

Not vulnerabilities in this repository:

- Limits already listed in [`docs/KNOWN-LIMITS.md`](docs/KNOWN-LIMITS.md), such as the web UI having
  no login (requests filed from it are never approvable) and the static token file `make demo`
  creates for its fictional people, which is for tests only.
- Vulnerabilities in Swarmada itself: report them to the
  [Swarmada repository](https://github.com/swarmada/swarmada).
- Vulnerabilities in other dependencies, unless the way this repository uses them makes them
  exploitable.
