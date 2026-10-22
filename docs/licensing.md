# Licensing and redistribution

Cantelop-authored platform code in this repository is licensed under
AGPL-3.0-only. The full terms are in [LICENSE](../LICENSE).

## License map

| Material | Terms |
| --- | --- |
| Runtime, guest helpers, gateway, dispatcher, builder, console, edge, CLI, shared packages, tests, scripts, and deployment definitions | AGPL-3.0-only unless an explicit exception below or an existing third-party notice applies |
| Cantelop-authored material under `examples/` and `console/examples/` | MIT; see the `LICENSE` in each examples directory |
| Cantelop-authored application source and configuration emitted by `cantelop project init` | MIT; see [the template grant](../licenses/MIT-EXAMPLES.txt). This grant covers the generated Cantelop material, not the user's additions or third-party dependencies |
| `clients/cli/cmd/cantelop/project_init_templates.go` | MIT; it carries an SPDX identifier for the template source |
| Separate `@cantelop/sdk` repository/package | Its existing MIT license; see [the SDK](https://github.com/stepandel/cantelop-sdk) |
| Third-party code, fonts, images, binaries, and dependencies | Their own terms; see [third-party notices](../THIRD_PARTY_NOTICES.md) |

The CLI imports runtime implementations and shared contracts. It is covered by
the platform license; it does not acquire the SDK's MIT license by using it.
The template grant lets users reuse the supplied starter content without
relicensing their own application under the platform license. Independently
check the terms of dependencies added to an application.

## Source and binary releases

CLI and Fire Fuse archives include the platform license, this license map,
third-party Go license texts, a dependency manifest, and `SOURCE.json` with the
exact source commit and repository URL. Their checksum files cover the notices
as well as the binaries. Release checks reject missing or altered notice files.
Fork maintainers set `CANTELOP_SOURCE_REPOSITORY` to their public GitHub-compatible
repository URL when building and validating archives.

Build public releases from a clean, reviewed commit using the existing release
qualification procedure. Publish that commit and any corresponding source
required by bundled components before advertising the artifact as publicly
redistributable. A source URL does not establish that a currently private
commit is publicly available.

The checked-in Go notice inventory covers the non-test Go packages in this
repository. Changes to `go.mod` or `go.sum` require refreshing the inventory and
reviewing the resulting licenses. The inventory records its generator version;
it is not a general certificate for arbitrary future builds.
Refresh with `python3 scripts/update-go-licenses.py`; it installs the pinned
audit tool into a temporary directory and unions the Linux/macOS amd64/arm64 builds.
Review warnings about assembly/native code and any additional target-specific
dependencies before distributing a new target.

JavaScript applications and tools resolve their own packages through the
committed lockfiles. Their license metadata inventory includes optional
platform packages. Refresh it with `node scripts/update-javascript-licenses.mjs`
after installing Console's development dependencies; missing versions are
resolved from the public npm registry without registry credentials.
For a bundled console/Worker distribution, review the actual
output and preserve applicable attributions and source obligations; package
metadata alone does not complete that review.

## Hosted and modified deployments

Follow the network-source provisions of AGPL-3.0 when running a qualifying
modified version. Offer corresponding source for the actual deployed revision
to users interacting with it, including local modifications and required build
and installation material. A link to an unrelated upstream revision is
insufficient. The public release checklist must verify the deployed source
offer and its accessibility before claiming this work is complete.
The console's `/source` page is linked from its footer and authenticated
navigation. It uses `CANTELOP_SOURCE_COMMIT` or Vercel's deployment commit,
and the same `CANTELOP_SOURCE_REPOSITORY` override. Production console builds
require an exact source revision. Other independently deployed components
still need their own matching source offer; a console page is not evidence
that a different runtime revision or unpublished local modifications are covered.

Private hosted console deployments identify the exact Vercel Git revision and
label their repository link as requiring operator access. Public snapshot
mappings verify source bytes and modes before production migrations; release
qualification and archives retain snapshot verification. Archives distinguish
the build commit from the corresponding source commit. See
[source publication](source-publication.md).

AGPL permits commercial use and competing hosting under its terms. Hosting
credentials, customer data, and private operational records are not source
release material. Keep them outside public source and release archives.

## Third-party services and images

Cloud service accounts are operational dependencies. They do not become part of
the platform's copyright license merely because Cantelop calls their APIs.
Review service terms separately.

MinIO, JuiceFS, Redis, Firecracker, guest kernels/root filesystems, and container
images have independent provenance and terms. Review the exact versions and
contents when distributing those binaries or images; the Go notice bundle
does not cover them. The patched JuiceFS source and modification must retain
upstream notices. MinIO's AGPL license does not, by itself, classify every
independent service communicating with it as a derivative work.

## Names and assets

The software license does not grant trademark rights to the Cantelop name or
logo. Descriptive references to the project are welcome; do not imply that an
independent deployment is operated or endorsed by Cantelop. Third-party marks
remain with their respective owners. Existing font licenses remain beside the
font files. Redistribution rights for other bundled visual assets must be
confirmed in the publication review.

## Contributions

Submit changes under the license applicable to the changed material and sign
off commits as described in [CONTRIBUTING.md](../CONTRIBUTING.md). A sign-off is
a certification of contribution rights, not a transfer of copyright.
