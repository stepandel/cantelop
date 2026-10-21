# Workspace data-only JuiceFS build

Cantelop carries `workspace-data-only.patch` against JuiceFS commit
`62bedf3cdecdb763c0be11edbab21f6764159aa5`. The patch is a small downstream
security policy, not an upstream JuiceFS release feature.

Run `storage/juicefs/prepare-juicefs-linux-amd64.sh [output-directory]` with Docker to
verify the source archive, apply the patch, run the in-memory regression suite,
and build the full Linux amd64 binary. The builder image is pinned by digest;
Go verifies dependencies against the upstream go.sum. The output includes a
binary checksum and source/patch/builder provenance. No backends are removed
from the build. Docker uses no host network, privileged mode, devices, live
storage, or credentials. The first build downloads the toolchain and modules.

## Policy

`juicefs mount --workspace-data-only` disables synthetic internal interfaces
in the VFS operation path, including direct inode/handle access. `.control`
and its prefixed alias are unavailable under every directory. Root-level
synthetic statistics/configuration/log/trash interfaces are denied; normal
Workspace files such as `.env`, `.git`, and `.config` remain available.
Administrative mounts without the flag retain upstream behavior. Use an
unexported trusted mount or metadata tooling for administration, never a guest
mount with management temporarily enabled.

Production therefore mounts the same volume a second time at the root-only
`/var/lib/fire-fuse/workspace-admin` path with the patched `--root-only` policy,
without `allow_other`, NFS export, or background jobs. Fire Fuse uses that view
only for mount-root, identity-bound
`juicefs rmr --skip-trash` operations after first quarantining the corresponding
directory through the data-only mount.

The flag emits filesystem type `fuse.juicefs-workspace-v1`. Before creating or
chowning a Workspace directory, Cantelop checks the kernel mount table for an
exact, full-filesystem mount with this type, root ownership, `allow_other`, and
`default_permissions`. Ordinary JuiceFS mounts, subdirectory views, and
ambiguous stacked entries fail attachment. The host mount administrator and
kernel remain trusted; the subtype is not cryptographic attestation against a
compromised host.

NFS exports use `subtree_check,all_squash` and anonymous UID/GID matching the
validated non-root workload identity. Managed Sessions retain `10001:10001`,
including file ownership. Multiple guest identities are intentionally mapped
to that one Workspace application identity; this does not preserve arbitrary
multi-user/chown semantics. Shared UID 10001 is not the tenant boundary.
Confinement comes from the export's client policy and subtree checks together
with denial of the internal management path.

## Qualification and rollout

Deploy the matching binary and mount service before enabling the new runtime.
An old or incorrectly configured running mount causes new attachments to fail;
installing the binary without replacing the mount is insufficient. Backup and
credential-rotation guards inventory both old and protected filesystem types.
Existing access-revocation ordering remains unchanged.

The local VFS tests cover internal names, synthetic IDs, alternate copy/link
paths, owner-only files, UID/GID, chmod, rename, hard links, normal dotfiles,
and the unaffected administrative mode. The FUSE test covers the policy
subtype. Go tests cover mount-table rejection and export ordering. The existing
opt-in KVM integration test adds NFS internal-name and owner-file checks.

The patched `config` command also accepts `--access-key-env` and
`--secret-key-env`. These explicitly apply `ACCESS_KEY` and `SECRET_KEY` from
the process environment without putting values in argv. The pinned upstream
`config` implementation ignores those environment variables unless an
explicit flag requests the update, so operators must use the matching patched
binary for storage rotation and restore.

These checks do not prove end-to-end NFS filehandle confinement on the deployed
kernel. Before release, separately qualify NFS handle attacks, positive sharing,
open-file rename behavior, pseudoroot traversal, and credential mapping on an
isolated Linux NFS/Firecracker fixture. Live qualification is not performed by
the build script. Do not silently disable subtree checking to resolve a
compatibility failure. Quotas and new storage-capacity limits are out of scope.

On an upstream update, rebase the patch and audit all inode/name/handle entry
points, synthetic node aliases, and FUSE mount construction. Preserve the
fail-closed runtime contract; change its version if the required policy changes.
