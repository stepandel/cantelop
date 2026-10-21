# Native workspace synchronization

The public capability is `workspace.sync({ localDirectory })`. Manifest, read,
write and remove operations are private implementation details, not separate SDK
capabilities. Sync uses the existing authenticated App Edge, outbound Worker,
origin broker, gateway and loopback Fire Fuse API. It never allocates a Sandbox.

The authenticated Workspace is the filesystem boundary. The SDK's optional
`remoteDirectory` selects a relative subtree, or the whole `/workspace` with an
explicit empty string. No persistent root binding, database migration, or
agent-specific directory policy is needed. Consumers such as Juiced can sync a
project folder while leaving account state in sibling folders. Whole-Workspace
sync includes account files unless the consumer excludes them. App integration
credentials remain App-wide: selecting a Workspace is not a new per-user or
per-Workspace identity grant.

The gateway has no storage mount. It validates the trusted App dispatch target
and Workspace ownership, then calls an explicit route on the existing loopback
runtime API with its existing scoped credential. Fire Fuse checks the ownership
and archival state again under a bounded Workspace operation lock. Archive marks
the resource unavailable before draining admitted transfers; purge runs only
after that drain. Filesystem I/O never holds the global resource mutex.
Hosted access rechecks the existing data-only JuiceFS mount policy and fails
closed when that mount is unavailable. No fallback storage directory is used.

## Local filesystem adapter

The SDK starts `cantelop internal-sync-files` as a private child process with a
caller-selected directory and a separate private metadata directory.
It reuses the same `workspacesync` implementation, avoiding unsafe JavaScript
path traversal and keeping macOS and Linux behavior consistent.

The helper prints one JSON handshake to its parent pipe with a numeric loopback
origin and a random credential. Every local HTTP operation requires that bearer;
no App, login, or infrastructure credential is loaded or forwarded. Parent pipe
EOF shuts down the listener and releases a native `flock` on the directory inode
itself. A process crash releases the lock automatically; Git clean and alternate
metadata directories cannot bypass ownership by replacing a lock file.

Local base hashes live in the user's Cantelop sync state directory, outside the
working tree. Metadata is never synchronized or added to Git. Temporary
`.cantelop-sync-*` publication files are excluded from both sides.
Polling and three-way hash reconciliation live in the SDK; the CLI helper does
not implement a second sync engine or an independently installed daemon.

## Wire contract

Protocol version 1 uses `/__cantelop/integration/v2/workspaces/{workspace_id}/sync`.
All operations accept an optional `root` query containing a canonical relative
path inside the Workspace. Explicit `root=` selects the whole Workspace; omission
retains `project/` for published SDK 1.1.0 clients. GET `op=scan&root=src/my-project`
returns `{ protocolVersion:1, root:"src/my-project", writes, entries }`. The root is
validated independently of entry paths; duplicates, absolute paths, traversal,
reserved entries, and symlink components are rejected. Each component is opened
through a pinned no-follow directory descriptor, including directory creation.
Each entry contains `path`, `version`, `size`, and `executable`. Directories are
implicit; empty directories, symlinks and special files are not synchronized in
version 1 and are omitted from the manifest. Hard links and oversized files fail
the scan. Manifests carry an explicit length and are bounded to 64 MiB encoded.
The version is SHA-256 of one executable byte (0 or 1) followed by file bytes.
GET `op=read&path=...&base=...` returns binary bytes only for that exact version.
PUT `op=write&path=...&base=...&executable=0|1` streams a whole file. `base=missing`
expresses creation; otherwise base is the expected content version. DELETE
`op=remove&path=...&base=...` removes a regular file matching the expected version.
Conflicts return 409 with `sync_conflict`. All responses prohibit caching.

Files are limited to 16 MiB, manifests to 10,000 entries / 128 MiB of hashed file
content, and operations to 30 seconds. Eight operations may enter storage at
once; a fixed set of locks serializes protocol operations on each Workspace.
Whole-file retries and polling are intentional first-version choices.

Both implementations exclude `.git`, `.cantelop`, `node_modules`, `.env*` and
reserved `.cantelop-sync-*` entries. These exclusions do not replace root
confinement or authorization. Direct reads and writes of unsupported links and
special files fail closed.

New files and directories inherit the owner, group and group-write mode of the
Workspace directory, so they remain writable by the Sandbox workload. Hosted
sync never creates the Workspace directory itself (the NFS export path owns it
and its workload identity), and scans prepare the complete selected subtree, including missing parents, only when mutations are
enabled. Writes to a hosted Workspace that has never been exported fail with
`sync_root_unavailable`.

## Concurrency and rollout

Expected versions detect already-diverged files; they are **optimistic checks,
not atomic compare-and-swap against arbitrary POSIX/NFS writers**. Atomic rename
avoids publishing a partially transferred file but does not close the interval
between checking a version and replacing the file.

Hosted mutation support is disabled unless the operator explicitly supplies
`-workspace-sync-writes`. Do not enable this flag on production JuiceFS/NFS until
concurrent write/delete/rename tests establish the intended overwrite contract.
There is no implicit fallback that bypasses this gate. The SDK reports the gate
instead of claiming an upload was synchronized. Local development and isolated
test roots enable mutations to qualify the complete client and transport.

Staging qualification must cover both transfer directions, an ordinary NFS
writer, interruption during upload, two clients modifying the same base, file
rename/type changes during publication, host restart, archive/purge during an
operation, and verification that neither host nor other Workspace files can be
read or modified. Resume always starts with a fresh manifest; no change journal
or unbounded event history is assumed.

## Verification

Run the native filesystem, private transport, archive and CLI tests with
`go test ./fire-fuse/workspacesync ./fire-fuse/gateway ./fire-fuse/controlclient
./fire-fuse/runtime/microvm ./fire-fuse/runtime/httpapi
./fire-fuse/runtime/cmd/fire-fuse ./clients/cli/...`.

After building the SDK worktree and CLI, run the optional cross-repository wire
test (ordinary local storage; no KVM host or credentials needed):

```sh
go build -o /tmp/cantelop-sync ./clients/cli/cmd/cantelop
CANTELOP_SYNC_SDK=/absolute/path/to/cantelop-sdk \
  CANTELOP_SYNC_HELPER=/tmp/cantelop-sync \
  go test ./tests/integration -count=1 -run TestWorkspaceSyncSDK -v
```

The test exercises `workspace.sync()`, the authenticated generated Edge adapter,
the actual Go file service, and the native local helper. It verifies binary
transfer, executable files, edits and deletes in both directions, persisted base
state, and divergent changes after reconnect. This does not replace real
JuiceFS/NFS concurrency qualification and does not enable hosted mutations.


To also verify Juiced's consumer and remote cwd, add
`CANTELOP_JUICED_DIRECTORY=/absolute/path/to/juiced` and run
`go test ./tests/integration -count=1 -run 'Test(WorkspaceSyncSDK|JuicedNamedProjectSync)' -v`.
The Juiced fixture verifies sibling account files stay remote and unchanged,
agent execution uses the synced project directory, HOME stays at the Workspace
root, and agent edits return to the local directory.
