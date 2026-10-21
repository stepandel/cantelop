# Pre-production release candidates

Cantelop prepares customer-facing artifacts without publishing or deploying
them. This boundary makes the exact SDK tarball and CLI binaries reviewable
before production credentials or infrastructure are involved.

## SDK

The SDK repository owns its npm package metadata and package qualification:

```sh
cd ../cantelop-sdk
corepack pnpm check
corepack pnpm test
corepack pnpm check:examples
corepack pnpm check:package
```

The final command packs and consumes `@cantelop/sdk` from a temporary tarball.
It does not contact the npm publishing API.

## CLI

The Cantelop repository owns versioned customer CLI archives:

```sh
make check-cli-release
make cli-artifacts
```

`clients/cli/VERSION` is the CLI version source of truth. The build embeds that
version and the full source commit, then packages macOS and Linux binaries for
amd64 and arm64. `SHA256SUMS` covers every archive. Qualification checks every
checksum and executes the native archive's `cantelop version` command.

## Fire Fuse deployments

Fire Fuse is internal infrastructure rather than a public package. It has no
semantic-version file and does not inherit the CLI version. Each build embeds:

- a deployment revision, defaulting to the first 12 characters of the source
  commit and optionally supplied as `CANTELOP_DEPLOYMENT_REVISION` by CI; and
- the full Cantelop source commit.

```sh
make check-fire-fuse-release
make check-fire-fuse-state
make fire-fuse-artifacts
```

The Linux amd64 archive is named with the deployment revision. It contains the
host binaries, a strict manifest with the revision and source commit, and nested
checksums. Artifact checksums are the immutable installation and rollback
identity. Qualification validates clean schema-v27 initialization, reopening
populated schema-v27 state, transactional upgrades from v24/v25/v26, encrypted
configuration, runtime checkpoints, and pre-cutoff schema rejection.

The operator CLI bundled for host maintenance still reports the public CLI
version because it is the same CLI program. That does not version Fire Fuse or
establish compatibility between the two components.

## Canonical starter

[`examples/hello`](../examples/hello) is the release qualification consumer. It
depends on the exact published SDK release-candidate version, commits its npm
lockfile, and contains both supported runtimes without a model provider or
secret. After the SDK is published, run:

```sh
make check-starter
```

The check ignores the operator's npm credentials, installs the committed graph
from the public registry with `npm ci`, verifies the installed SDK version,
builds the Edge API, builds the Session runtime image for `linux/amd64`, boots the
image, delivers a Message, verifies handler settlement, and waits for covered
runtime quiescence. It requires the starter dependency to match the SDK
repository version and deletes the
temporary container, image, and project afterward.

The committed starter keeps both the registry dependency and its lockfile.
This prevents a workspace symlink from entering a customer Docker build and
binds qualification to the same dependency graph customers install.

## Complete qualification

After both repositories are committed, clean, and on `main`, run the final
pre-production gate from this repository:

```sh
make qualify-release
```

The CLI and SDK are independently versioned public packages. Fire Fuse is
identified independently by deployment revision, source commit, and checksum;
there is no equality requirement between package and deployment identities.
The SDK must match the canonical starter dependency. Qualification runs the
full Cantelop suite, full SDK suite and examples, packed-package consumer test,
Fire Fuse state qualification, and containerized starter qualification. It
then builds the SDK tarball, four CLI archives, and deployment-revisioned Fire
Fuse archive once in the final candidate directory. CLI and Fire Fuse release
qualification validates those exact archives instead of rebuilding temporary
copies. Nested checksums, logs, and an `evidence.json` containing package
versions and the deployment identity are written to a new ignored directory
under `dist/release-qualification`.

Every qualification log begins and ends with UTC timestamps and records the
step's elapsed seconds and exit status. This makes repeated qualification runs
comparable without changing the release gates or performing production work.

The evidence binds both source commits and explicitly records
`production_operations_performed: false`. Qualification fails if either
repository starts or ends dirty. The output directory is never reused, so an
earlier candidate cannot be silently overwritten.

## Production boundary

The following are deliberately excluded from pre-production qualification:

- npm publication or registry credentials;
- Git tag creation or remote pushes;
- GitHub release creation and artifact upload;
- macOS signing or notarization credentials;
- installer/homebrew publication;
- console, Cloudflare, Hetzner, database, or worker deployment.

Those operations begin only after the release candidates and qualification
evidence have been reviewed.

See [deployment](./deployment.md#1-select-and-qualify-one-cohort) for the evidence
required before any of those operations. Qualification is not authorization to
perform them.
