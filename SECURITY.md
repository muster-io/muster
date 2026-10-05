# Security policy

## Reporting a vulnerability

Report a vulnerability privately, through GitHub's private vulnerability reporting: open the
[Security tab](https://github.com/muster-io/muster/security/advisories) of the repository and choose **Report a
vulnerability**, or go straight to <https://github.com/muster-io/muster/security/advisories/new>. Do not open a public
issue, pull request or discussion about it.

Include in the report:

- a description of the vulnerability and its impact;
- steps to reproduce it, or a proof of concept;
- the affected versions;
- a suggested fix, if you have one.

## Response process

We acknowledge a report within 7 days and agree with you on a disclosure date. The fix ships as a new patch release of
the latest minor release, together with a GitHub security advisory, which can carry a CVE that GitHub assigns. Tell us
if you would like to be credited in the advisory.

## Supported versions

Only the latest minor release is supported: a security fix ships as a new patch release of it, and older minor
releases are not patched. While v1.2 is the latest minor release, for example, a fix ships as the next v1.2.z, and v1.1
and v1.0 receive nothing. Before 1.0 the same holds for the latest 0.y release.

## Supply-chain checks

- On every pull request and every push to `master`, CI runs govulncheck, which fails on a known vulnerability in Go
  code that Muster calls, and `pnpm audit --prod`, which fails on a high or critical advisory in the production
  dependencies of the web UI. The nightly workflow runs both again.
- In the same CI runs, the dependency licence checks fail when the binary or the web UI embedded in it depends on code
  outside the permissive licences that the project allows; the licences of build tools are reported.
- CodeQL analyses the Go and TypeScript code on every pull request, on every push to `master` and weekly.
- OpenSSF Scorecard evaluates the repository's practices on every push to `master` and weekly.
- CodeQL and Scorecard findings appear in the repository's code scanning alerts.
- Renovate opens pull requests that update dependencies; versions are pinned, GitHub Actions by commit SHA.
