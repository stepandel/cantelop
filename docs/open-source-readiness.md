# Open-source readiness

Updated 2026-10-21. The maintainer authorized a fresh public source repository
as `stepandel/cantelop`, renaming the current private repository to
`stepandel/cantelop-internal` and retaining Vercel there. This is a source preview;
independently qualified self-hosting remains pending.

## Selected publication boundary

Publish only the reviewed current source snapshot. Existing private Git history,
issues, Actions logs, tags, releases, operational evidence, and actual inventory
values remain private. Public contributions run credential-free checks and are
reviewed before promotion into hosted deployments. See the
[publication procedure](source-publication.md) and explicit export policy.

Public snapshot commit IDs differ from private deployment commits. The publication
pin and complete file manifest bind them by source bytes and executable modes;
the Vercel build verifies the mapping before running migrations. Deployment and
signing jobs require the private repository's immutable ID.

## Prepared changes

- AGPL-3.0-only platform license and an explicit MIT grant for starter content,
  examples, and the template source; the separate SDK remains MIT.
- A license map, third-party notice texts, versioned dependency inventories,
  and supported refresh commands for Go and JavaScript dependencies.
- CLI and host archives include licenses and exact source identities. Checks
  verify notice contents in the archives, including every CLI platform target.
- An exact-revision console source page, linked from public and authenticated
  navigation. Production builds require a source revision; fork operators can
  configure their own source repository.
- Public contribution CI with read-only permissions, disposable PostgreSQL,
  pinned tools, immutable Action references, and a full-history secret scan.
  No deployment or signing secrets are assigned to the new workflow.
- Contribution/sign-off and private security-reporting guidance, plus code-owner
  review rules for licensing and release-authority files.

## Disclosure review

The baseline was `042fec76ed35fb62485008d3f428a4bae99cea40`. A private remote
mirror covered 2 branch refs, 48 tags, and 366 pull-request refs, totaling 3,082
unique reachable commits. Gitleaks 8.30.1 found seven historical matches, all
triaged as public commit identities, placeholders, operation IDs, or fixed test
tokens. Only those exact fingerprints are excluded from future history scans.
The reviewed history passes with inline allow comments disabled.

The review also covered 177 retained Actions log archives, 135 textual release
assets, issue/PR descriptions and comments, and all 184 historical CLI archives
across 46 releases. Every archive matched its published SHA-256. Executable
strings and bundled README text produced no secret-scan matches. Retained-log
matches were public signing fingerprints and cache content hashes.

No confirmed live credential was identified. A source-snapshot scan has six
known fixture/placeholder/hash matches requiring the same triage. The 227
unavailable run-log downloads remain a coverage limitation. This is a
disclosure review, not an exhaustive vulnerability or binary security audit.
Detailed account findings and raw evidence stay outside the source repository.

## Remaining gates before publication

| Gate | Required evidence/action |
| --- | --- |
| Preview isolation | Keep the public repository disconnected from Vercel and free of hosted credentials; private previews remain restricted to trusted code |
| Repository protection | Enable required contributor/secret checks and code-owner review on the public repository; private protection remains limited by the current account plan |
| Private reporting | Enable and verify a usable private vulnerability-reporting channel; documentation alone does not create it |
| Ownership and assets | Maintainer confirms authority to license contributed code and redistribute bundled visual assets |
| Historical operations records | Excluded through a new public Git root and the explicit source export policy; never mirror private refs |
| Exact candidate | Scan and check the fresh public snapshot, verify the public/private file mapping, and publish no unqualified binary artifacts |
| Publication verification | After the controlled visibility change, verify anonymous source access, the actual deployed source offers, protected release authority, and a real fork CI run |

Code-level checks cannot prevent attacker-controlled build code from reading a
credential injected by an external deployment service. The separate public
repository must have no hosted deployment connection or credentials.

## Validation and release boundary

The Go, Console, Edge, Python, Ansible, architecture, CLI/API contract, license
inventory, and Terraform-format checks have been exercised locally. Database
tests that need a disposable PostgreSQL service passed in Contributor checks
on merged main `55d62961d9705c839c19a9fa9e32ff5a5b4b53f1`.
The local real-KVM/provider qualification remains separate.

Draft CLI and host archives have been built and checked locally. They are
private verification outputs from the working tree, not public release assets.
The final release must bind artifacts to the actual clean candidate commit.

Milestone B still requires configurable deployment identities, one supported
installation procedure, and a fresh independently qualified deployment. See
the [release plan](open-source-release-plan.md) for its acceptance criteria.
