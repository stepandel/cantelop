# Reporting security issues

Please do not disclose credentials, customer information, exploit details, or
unpatched vulnerabilities in public issues or pull requests.

Use [GitHub's private vulnerability reporting](https://github.com/stepandel/cantelop/security/advisories/new)
for the public source repository. If that channel is unavailable, contact the
repository owner privately through an existing trusted channel and ask how to
transfer the report securely. Do not assume that a public issue or an
unsolicited attachment is private.

Include the affected component and exact revision, prerequisites, reproducible
steps, expected and observed behavior, and likely impact. Use synthetic data;
do not include live secret values. Report only what is needed to reproduce the
issue. Testing against hosted Cantelop requires the operator's authorization;
use an isolated installation for intrusive checks.

The source preview has no promised response-time SLA or long-term supported
release series. Report the revision you tested. Maintainers will coordinate
triage, remediation, and disclosure privately where possible.

The earlier [single-host security audit](docs/security-audit.md) describes its
own scope and revisions. It is not a certification of the current complete
platform or of an independently configured deployment.
