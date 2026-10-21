# Storage

The storage host serves Workspace data to compute hosts over the
compute-to-storage WireGuard tunnel. It runs no Cantelop-authored service;
it runs pinned third-party builds that this directory produces:

| Component | Build | Role on the host |
|---|---|---|
| JuiceFS (workspace data-only) | [`juicefs/prepare-juicefs-linux-amd64.sh`](juicefs/prepare-juicefs-linux-amd64.sh) | Formats and administers the Workspace volume |
| MinIO | [`minio/build-minio-linux-amd64.sh`](minio/build-minio-linux-amd64.sh) | JuiceFS object backend |
| Redis | Distribution package | JuiceFS metadata engine |

Compute hosts also mount the volume with the same patched JuiceFS client; see
[`juicefs/README.md`](juicefs/README.md) for the data-only mount policy.
