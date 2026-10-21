# Third-party notices

Cantelop-authored platform code is AGPL-3.0-only with the exceptions in
[docs/licensing.md](docs/licensing.md). Third-party materials retain their own
licenses and copyright notices.

## Go executables

The CLI and host binaries use the Go standard library and the following
modules. Exact versions, upstream license URLs, and notice checksums are in
[`licenses/go/modules.json`](licenses/go/modules.json); full license texts are
stored beneath [`licenses/go/`](licenses/go/).

| Module | License |
| --- | --- |
| github.com/coder/websocket | ISC |
| github.com/dustin/go-humanize | MIT |
| github.com/google/uuid | BSD-3-Clause |
| github.com/mattn/go-isatty | MIT |
| github.com/ncruces/go-strftime | MIT |
| github.com/remyoudompheng/bigfft | BSD-3-Clause |
| golang.org/x/sys | BSD-3-Clause |
| modernc.org/libc | BSD-3-Clause |
| modernc.org/mathutil | BSD-3-Clause |
| modernc.org/memory | BSD-3-Clause |
| modernc.org/sqlite | BSD-3-Clause |

The Go standard library license is included as `licenses/go/GO-LICENSE`.
Keep these texts with distributed executables. This inventory covers the
current non-test packages; the listed notices are a conservative bundle across
the supported executable targets rather than a claim that each binary links
every module.

## JavaScript packages and fonts

Console, Worker, example, and benchmark packages retain their upstream terms.
Use their committed lockfiles and [license metadata inventory](licenses/javascript-packages.json)
to identify the resolved versions. Preserve license files shipped in installed
packages. A separately distributed bundle needs review of its actual contents,
including native libraries and generated assets.

The console includes Geist and Sora font files under SIL Open Font License 1.1.
Their notices are in `console/web/assets/fonts/OFL.txt` and
`console/web/assets/fonts/OFL-Sora.txt`.

JavaScript license metadata includes components under MPL-2.0, LGPL-3.0-or-later,
and CC-BY-4.0 as well as permissive software licenses. This file does not replace
their required notices or corresponding-source obligations when applicable.

## Independently installed software

| Component | Source/provenance | Review boundary |
| --- | --- | --- |
| Firecracker and jailer | Version/checksum in `scripts/prepare-firecracker-linux-amd64.sh` | Preserve upstream Apache-2.0/BSD notices when redistributing |
| JuiceFS | Pinned revision, source checksum, and patch in `storage/juicefs/` | Preserve upstream Apache-2.0 notices and identify downstream changes |
| MinIO | Pinned release in `storage/minio/build-minio-linux-amd64.sh` | AGPL-3.0; review corresponding-source requirements for any distribution |
| Redis | Distribution package installed by the storage role | Record the exact package/version and its applicable terms |
| Guest Linux kernel and platform/rootfs | Sources and preparation in `platform/hetzner/prepare-guest.sh` | Review GPL and distribution/package notices for the actual image contents |
| Application container images | Chosen by each operator/application | Review the actual image and its constituent packages before redistribution |

These components are not silently relicensed by the Cantelop platform license.
The first public release must retain an artifact-by-artifact disposition for
any third-party binaries or images it distributes.
