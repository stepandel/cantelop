# Docker image runtime

Cantelop materializes Docker-style workloads into immutable, content-addressed
Builds. The trusted image catalog maps each `sha256:<digest>` Build ID to a
rootfs artifact and process contract. Apps reference Build IDs directly; there
is no separate logical Image resource.

Each Firecracker VM boots the platform rootfs and receives the selected workload
rootfs as `/dev/vdb`. The guest mounts the image read-only beneath a writable
OverlayFS layer backed by a 128 MiB tmpfs, supplies `/dev`, `/proc`, `/sys`, DNS,
and the Workspace mount, then executes the catalog process inside a private
mount namespace. The overlay is discarded with the VM.

## Materialize a Docker image

Customer deployments upload a standard uncompressed archive produced by
`docker image save`. The CLI resolves tags to an immutable local `sha256:` image
ID before saving, so the archive carries no mutable repository tags. The archive
must contain exactly one `linux/amd64` image,
and the CLI supplies its managed Session process contract. Customers do not
need to declare a runtime `USER`, `WORKDIR`, `ENTRYPOINT`, or `CMD` in their
Dockerfile. The CLI creates this archive locally; customers never upload a
Build ID, version, rootfs path, or host manifest.

Create the provisioning App first, add its slug and the API/Session runtime source paths
to `cantelop.json`, then use `cantelop build` to qualify locally or
`cantelop deploy` to build and release. The unified build path creates a
`linux/amd64` image; deployment binds both API and Session runtime artifacts to one
release.
See `docs/cli.md` for the complete workflow.

The control/build VM downloads and verifies the compressed archive from R2.
Its root-owned converter accepts a single operation ID over a restricted local
Unix socket; it has no database or R2 credentials. The converter validates the
Docker archive, addresses the loaded image by its verified configuration digest
rather than a mutable tag, and creates—but never starts—a container. It exports
the flattened filesystem, extracts it without allowing path traversal, and
creates the immutable ext4 rootfs with a tree-derived UUID/hash seed and fixed
e2fsprogs build clock. The control worker uploads the ext4 file and a portable
manifest to R2, then sends Fire Fuse a signed download URL. Fire Fuse verifies
the complete ext4 digest and size before installing it and activating the App
Build. The compute host does not run Docker or retain the source archive.

Prepared ext4 files and manifests have no automatic R2 expiry. They remain
available until explicit reference-aware retirement so a replacement compute
host can reinstall a referenced Build. Failed conversions do not activate a
Build or consume an App Build version.

The following operator-only command remains useful for preparing fixtures and
manually reviewed Builds:

On Linux, pull or build the source image and run:

```sh
sudo ./platform/hetzner/prepare-docker-image.sh \
  IMAGE_REFERENCE \
  /srv/fire-fuse-image-inputs/application.ext4 \
  /srv/fire-fuse-image-inputs/application.process.json
```

The command resolves Docker's final image configuration and flattened
filesystem. It rejects unsafe or unsupported process contracts before writing
the ext4 artifact and process metadata.

The catalog entry contains the immutable rootfs identity and process contract:

```json
{
  "schema_version": 1,
  "images": {
    "sha256:BUILD_DIGEST": {
      "rootfs_image": "/var/lib/fire-fuse/images/sha256-ROOTFS_DIGEST.ext4",
      "rootfs_sha256": "ROOTFS_DIGEST",
      "rootfs_size_bytes": 1073741824,
      "process": {
        "args": ["/app/server"],
        "env": ["PATH=/usr/local/bin:/usr/bin:/bin"],
        "working_directory": "/app",
        "uid": 1000,
        "gid": 1000
      }
    }
  }
}
```

Build identity covers both the rootfs and process contract. Changing arguments,
environment, working directory, UID, GID, or rootfs identity produces a new
Build ID.

## App-specific settings

Image process environment is immutable Build metadata. App-specific settings
and credentials should not be baked into a Build. Customers manage App-level
variables and write-only secrets with the CLI. The same App configuration is
bound to its API Worker and resolved for each newly started Session in
this order:

1. immutable Docker image `ENV` defaults;
2. App variables and secrets;
3. managed Session identity, home, temporary, and XDG environment values.

App values override ordinary image defaults by name. Changing configuration does not
restart or alter running instances.

## Publish a Build

The installed `fire-fuse-image` command publishes an immutable Build into the
trusted catalog. A strict manifest wraps exactly one catalog image:

```json
{
  "schema_version": 1,
  "image": {
    "rootfs_image": "/var/lib/fire-fuse/images/sha256-ROOTFS_DIGEST.ext4",
    "rootfs_sha256": "ROOTFS_DIGEST",
    "rootfs_size_bytes": 1073741824,
    "process": {
      "args": ["/app/server"],
      "env": ["PATH=/usr/local/bin:/usr/bin:/bin"],
      "working_directory": "/app",
      "uid": 1000,
      "gid": 1000
    }
  }
}
```

Publish it while holding the normal host maintenance boundary:

```sh
sudo /usr/local/bin/fire-fuse-image \
  -manifest /var/lib/fire-fuse/image-inputs/application.manifest.json
```

The command validates trusted ownership and paths, derives the Build ID, and
atomically adds the entry. Re-publishing identical metadata is idempotent;
conflicting metadata for an existing Build ID is rejected.

Publishing does not select the Build for any App. An App-scoped activation
stores the Build ID in `app_builds` and updates the App Session runtime's
`active_build_id`. A newly created provisioning App has neither until its first
Build succeeds.

Within an App, the control plane assigns successful Builds chronological
versions beginning at 1. Versions are not accepted from customers, failed
uploads consume no version, and an identical attachment replay returns the
existing version.

### Automatic host Build retention

When admitting a new prepared Build would reach the 25-Build per-App limit or the
250-Build host limit, Fire Fuse prunes eligible older host Build attachments
for that App in the same transaction as upload admission. It keeps the newest
20 versions, any Build selected by an App Session runtime, and any Build used
by a Session whose Sandbox has not terminated. A missing Sandbox or a live
Sandbox without a Session binding prevents unsafe cleanup. Historical Sessions
whose Sandboxes have terminated do not pin old attachments. The highest
version is always retained, so version numbers continue increasing.

Retired successful host operations become terminal `build_retired` receipts;
their command IDs remain reserved, preventing delayed retries from recreating
or activating retired Builds. PostgreSQL release history, published catalog
images, and rootfs/artifact files remain available. This retention policy frees
attachment quota; it does not delete files itself. Disk space comes back through
automatic collection, when enabled, or the manual retire-and-collect procedure.

At the host limit, admission first retires unreferenced older attachments from
archived Apps, only as many as it needs, before trimming the requesting App. It
preserves each archived App's highest version and applies the same active
runtime and live Sandbox checks; unbound Sandboxes do not block it because
archived Workspaces cannot start Sessions. Attachments from other active Apps
remain untouched.

If protected Builds or host capacity still prevent admission, the whole
transaction rolls back and the deployment fails with `build_retention_quota`.
The control worker reports this error immediately instead of retrying it as
ordinary rate limiting.

For customer deployments, catalog publication precedes one atomic SQLite
transaction that records operation success, inserts or reuses the App Build,
assigns its chronological version, updates the active Session runtime Build, and
makes the first Build available to the App's Workspaces. A crash after catalog publication
simply republishes the same immutable metadata and retries that transaction.

### Host disk headroom

Before downloading a prepared image, the Build ingress checks the image
filesystem. It needs room for a full temporary copy of the image and must still
leave 5% of the filesystem free, at least 4 GiB and at most 32 GiB, because the
lifecycle database, VM jails, and catalog rewrites share that disk. A shortfall
fails before any bytes are written, with HTTP 507 `host_disk_capacity`. The
control worker cancels the pending upload and fails the deployment instead of
retrying, because the disk only frees up when an operator or the automatic
collector removes images.

Fire Fuse removes leftover `.prepared-*.ext4` partial downloads from the image
directory when it starts, before it accepts any upload.

## Retire and collect a Build

Retirement removes an unreferenced Build from the catalog:

```sh
sudo systemctl stop fire-fuse.service
sudo /usr/local/bin/fire-fuse-image -retire sha256:BUILD_DIGEST
```

Retirement fails while any App Build or non-terminated internal Sandbox
references the Build. It returns the rootfs path but never deletes the file.

After retirement, garbage collection explicitly selects the digest-derived
rootfs path:

```sh
sudo /usr/local/bin/fire-fuse-image \
  -gc-rootfs /var/lib/fire-fuse/images/sha256-ROOTFS_DIGEST.ext4
```

Collection rejects paths outside the image root, symlinks, digest mismatches,
published references, App Build references, and non-terminated Sandbox
references. It never scans a directory or accepts a glob.

### Automatic collection

Fire Fuse can retire unreferenced customer Builds and delete their rootfs files
while it runs. It is off by default. Enable it by adding a grace period of at
least one hour to the `ExecStart` line in
`platform/systemd/compute/fire-fuse.service`:

```sh
-image-collection-grace 24h
```

Every 15 minutes the collector takes the catalog writer lock and checks every
digest-named `sha256-*.ext4` entry in the image directory. A Build is in use
while an unarchived App attaches it or selects it as its Session runtime, or a
live Session or non-terminated Sandbox uses it. Archived App attachments and
runtime selections do not keep images in use after their Sandboxes terminate.
Once every check for the full grace period found a Build unused, the collector
removes it from the catalog. It then
re-reads references and puts back any Build that became used meanwhile. Only
then does it delete the rootfs, and only when no remaining entry shares the
file. Running VMs keep their bind-mounted copy, so deletion cannot disturb
them.

Without a catalog entry, a file can't be traced to a Build. The collector
also deletes digest-named rootfs files that no catalog entry references after
the same grace. They come from manual retirement without collection or from a
crash between the catalog rewrite and the deletion. This sweep pauses while any
in-use Build is missing from the catalog.

The collector skips a pass while any Build upload is pending, because an
install publishes its Build before attaching it, and while a runtime deployment
holds the catalog. Grace timers live in memory, so a restart starts them again.
Profile kernels and rootfs images, and catalog entries outside the image
directory, are never collected.

A customer rollback to a release whose Build the host no longer stores fails
immediately with HTTP 410 `target_build_missing`. The console disables that
rollback and `cantelop rollback` explains it. The console reads the App's
retained Builds through the observer's `GET /apps/{id}/builds`. When the
observer can't answer, the activation worker checks the compute ingress's
`GET .../apps/{app_id}/builds/{build_id}` before closing release admission, so
the live release keeps serving either way.

Choose a grace longer than the gap between publishing a Build manually and
attaching it. Enabling collection also affects runtime rollback: a rollback
snapshot restores the prior lifecycle database and catalog but not image
files. After rollback, Builds collected since the upgrade fail to launch until
they are deployed again. Keep collection disabled during a rollback window, or
use a grace longer than that window.

Publication, retirement, promotion, and collection share the catalog writer
lock. Publication verifies the installed rootfs while holding that lock, so
collection cannot remove it between verification and catalog publication.
Promotion also holds the deployment lease so catalog state cannot change
between staged validation and activation.

## Runtime contract

Every App references a content-addressed Build ID; there is no profile fallback.
The platform-owned runtime profile selects the VM shape and matching
kernel/rootfs snapshot. Host configuration owns admission capacity, and each
Session request supplies its explicit `keep_alive_seconds` idle policy. The App
Session runtime supplies the runtime profile, active Build ID, zero-prewarming policy,
and platform-assigned private listener port inherited by all of its Workspaces.

The image's lower filesystem is read-only. Writes elsewhere in the container
use the ephemeral 128 MiB overlay and remain subject to Unix permissions.
Durable writes belong under `/workspace`, exposed as `CANTELOP_WORKSPACE_PATH`.
Every newly built Session starts in `/workspace` as UID/GID `10001:10001`.
The CLI reserves `/__cantelop` for its loader, writable home and temporary
folders, and `/opt/cantelop` for the bundled Session module. System packages and
customer assets stay outside those paths and `/workspace`.

Versioned Session images carry `CANTELOP_RUNTIME_CONTRACT=1`. The materializer
requires that contract for new platform-Bun uploads and validates the resolved
process before catalog publication. The guest validates the same contract,
mounts the platform Bun executable read-only, and launches it through the
CLI-supplied loader with package auto-installation disabled. App environment
merging reapplies the managed values last. Image `VOLUME` and pending `ONBUILD`
settings are rejected; health checks, stop behavior, and routing belong to the
platform.

Roll out the host validation and updated guest runner/snapshot before releasing
the matching CLI. Existing unversioned catalog Builds retain their identity,
working directory, and launch behavior until rebuilt. Rebuilding a legacy custom
image switches it to UID/GID `10001:10001`: the NFS manager assigns the Workspace
root to that identity, but existing descendant files retain their ownership.
Assess ownership compatibility before moving an existing Workspace from a
legacy custom UID/GID; the rollout does not recursively rewrite durable files.
Initial scope remains deliberately narrow: Linux `amd64`, one workload process
per microVM, no runtime registry pull, and no privileged container features.
