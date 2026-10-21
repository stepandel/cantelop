# Workspace storage backup and retention

This document defines the backup contract for the initial shared JuiceFS
volume. It is an operations responsibility, not a fire-fuse orchestrator
responsibility.

Redis metadata and MinIO objects live on the dedicated storage host, while
the Fire Fuse compute host runs the JuiceFS client and NFS exports. Redis
AOF/RDB persistence and MinIO's data directory protect against some process
failures, but neither is an off-host backup. Losing the storage disk can lose
both halves of every workspace. Storage-host operations, including compute-host
replacement, are in [operations](operations.md); the completed one-way migration
is recorded in [history/storage-host-cutover.md](history/storage-host-cutover.md).

## Backup unit

A recoverable JuiceFS backup is one indivisible set containing:

1. A quiesced JuiceFS binary metadata dump.
2. A point-in-time copy of the corresponding MinIO bucket contents.
3. A Redis persistence snapshot for engine-specific recovery.
4. A manifest with creation time, component versions, volume identity, object
   snapshot identity, file sizes, and SHA-256 checksums.
5. The encrypted configuration material needed to restore the storage
   credentials, source identity, and pinned component versions.

Metadata and object data must never be retained or expired independently. A
metadata dump without its referenced objects is not a filesystem backup, and
MinIO objects without matching metadata cannot reconstruct paths and file
contents.

The host JuiceFS gateway is the only filesystem client. This deployment does
not rely on automatic metadata backups; every retained set must contain an
explicit `juicefs dump --binary`.

## Consistency procedure

The initial implementation uses a bounded maintenance window:

1. Enter an operator-controlled maintenance window so API clients stop
   submitting VM creation requests.
2. Gracefully terminate all workspace-enabled VMs.
3. Stop `fire-fuse.service`, NFS, and both compute-host JuiceFS mounts.
4. Confirm no JuiceFS client session remains for the volume.
5. Run the pinned client against the root-only metadata URL:

   ```sh
   juicefs dump META-URL metadata.dump --binary
   ```

6. Request and verify a Redis persistence checkpoint.
7. Create the MinIO bucket snapshot and Redis snapshot without allowing
   workspace writes to resume.
8. Encrypt and verify the restore configuration with a dedicated, separately
   retained Ansible Vault password.
9. Generate and checksum the backup-set manifest.
10. Transfer the complete set to the off-host destination and verify it there.
11. Restart both compute-host JuiceFS mounts and NFS, then restart fire-fuse and
    require `/readyz`.

`juicefs dump` is not snapshot-consistent while the filesystem is changing.
Draining all clients is therefore part of correctness, not merely an
operational preference.

The metadata dump must omit object-store secrets. The backup manifest must not
contain the Redis password, metadata URL, MinIO secret key, Ansible Vault
password, or off-host backup credential.

`restore-secrets.vault` is a whole-file Ansible Vault ciphertext containing the
source volume identity, Redis and MinIO credentials, JuiceFS object access
credential, captured component versions, and pinned JuiceFS/MinIO binary
checksums. Its dedicated Vault password is a recovery root of trust: keep it
outside the storage host and outside the backup destination, and test that the
recovery operators can retrieve it. The capture process passes plaintext only
through Ansible process memory and verifies an exact encrypt/decrypt round
trip before writing the ciphertext.

### Backup-set manifest

Each set uses a strict, versioned `manifest.json`. Version 1 has exactly this
shape:

```json
{
  "schema_version": 1,
  "set_id": "backup-20260728T120000Z",
  "created_at": "2026-07-28T12:00:00Z",
  "source": {
    "volume_name": "fire-fuse-workspaces",
    "volume_uuid": "01234567-89ab-cdef-0123-456789abcdef",
    "juicefs_version": "CAPTURED_JUICEFS_VERSION",
    "redis_version": "CAPTURED_REDIS_VERSION",
    "minio_version": "CAPTURED_MINIO_VERSION",
    "object_snapshot_id": "snapshot-20260728T120000Z"
  },
  "artifacts": [
    {
      "component": "juicefs_metadata",
      "path": "metadata.dump",
      "size_bytes": 1234,
      "sha256": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
    },
    {
      "component": "minio_objects",
      "path": "minio-objects.tar",
      "size_bytes": 1234,
      "sha256": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
    },
    {
      "component": "redis_persistence",
      "path": "redis-persistence.tar.gz",
      "size_bytes": 1234,
      "sha256": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
    },
    {
      "component": "encrypted_restore_configuration",
      "path": "restore-secrets.vault",
      "size_bytes": 1234,
      "sha256": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
    }
  ]
}
```

The four artifact components are mandatory and may appear only once. Artifact
paths are relative to the manifest, and every artifact must be a regular file,
not a symlink. Unknown fields are rejected so credentials cannot be added to
the plaintext manifest accidentally.

Verify the complete local set before and after off-host transfer:

```sh
python3 platform/ansible/scripts/verify-storage-backup.py \
  /path/to/backup-set/manifest.json
```

The verifier checks the schema, required component set, paths, sizes, and
SHA-256 checksums. It does not create a backup or establish that the set is
off-host, immutable, or restorable.

Manifest construction uses only the four verified artifact facts and an
explicit allowlist of source fields. The candidate is scanned for the active
Redis metadata URL and storage credentials, then passed to the same strict
verifier before `manifest.json` is promoted atomically. The manifest is the
local completeness marker; a staging directory without it must never be
uploaded or retained as a completed set.

The local capture playbook wraps maintenance entry through manifest creation
with an exclusive host lock and an unconditional fire-fuse restart/readiness
check. It does not drain VMs, upload the resulting set, or apply retention.
Incomplete sets remain under the staging root for diagnosis. The lock is
released only after a successful set verifies again with fire-fuse restored;
a retained lock therefore requires operator inspection rather than blind
deletion.

Off-host transfer is a separate retryable operation so network duration does
not extend the maintenance window. It accepts only a validated set ID and
derives the local path beneath the fixed staging root. Before writing, it
reverifies both the complete local set and the destination's versioning,
COMPLIANCE default retention, and restricted credential boundary.

The four artifacts are uploaded first. Each resulting object version is
streamed back through SHA-256 and must report the required COMPLIANCE retention
period. `manifest.json` is uploaded and verified last, making it the remote
completeness marker. A retry after a partial attempt may create newer retained
artifact versions. When a remote manifest already exists, retry switches to
read-only reconciliation: it streams and verifies that manifest and every
current artifact version without writing anything.

Successful transfer or reconciliation atomically publishes a root-only local
record under `/var/lib/fire-fuse/storage-backups/completed/`. That record binds
the set ID and local manifest checksum to the remote prefix, object version
IDs, SHA-256 values, and retention timestamps. It is the local promotion
marker; the actual backup bytes remain unchanged beneath the staging root.
Existing records are accepted only when they exactly match newly verified
evidence. Transfer and bookkeeping never delete an object or local backup
file.

The local inventory auditor is the mandatory read-only gate before future
retention work. It classifies safe incomplete captures, complete local sets,
transferred sets with matching completion records, and inconsistent entries.
The last category includes malformed manifests, permission failures,
mismatched evidence, unexpected files, and orphan completion records. The
auditor emits only set IDs, states, and bounded reason codes; it does not expose
credentials or remote configuration details. An inconsistent result fails the
Ansible audit play and authorizes no automatic repair or deletion.

## Off-host destination contract

The first supported transfer boundary is an S3-compatible object store in a
different failure domain. The remote bucket is provisioned by storage
operations, not fire-fuse, and must provide:

- a different failure domain from the KVM host and its disks;
- encryption in transit and at rest;
- immutable or separately versioned backup sets;
- integrity verification after upload;
- credentials dedicated to backup, with no access to fire-fuse;
- lifecycle rules that expire only complete backup sets; and
- enough capacity for the documented retention window.

A local directory, another path on the MinIO disk, or Redis AOF/RDB files alone
does not satisfy this contract.

The host-side non-secret descriptor is strict JSON:

```json
{
  "schema_version": 1,
  "driver": "s3",
  "endpoint_url": "https://s3-backup.example.com",
  "region": "example-1",
  "bucket": "fire-fuse-storage-backups",
  "prefix": "shared-volume/backup-sets",
  "credentials_file": "/etc/fire-fuse-storage/backup-s3-credentials.json",
  "immutability_mode": "object_lock"
}
```

The separate credential file has mode `0600`, is owned by root, and has exactly
this shape:

```json
{
  "schema_version": 1,
  "access_key_id": "dedicated-backup-access-key",
  "secret_access_key": "dedicated-backup-secret-key"
}
```

Store the credential values in Ansible Vault; do not commit either live file.
Validate both files on the storage host:

```sh
python3 platform/ansible/scripts/validate-storage-backup-destination.py \
  /etc/fire-fuse-storage/backup-destination.json
```

Validation proves the local descriptor is safe to consume. It does not prove
that the endpoint is in another failure domain, the credential is restricted,
or Object Lock is enabled remotely; the later destination qualification must
test those properties before the first upload.

Before uploading any backup, run the read-only remote qualification with the
same pinned MinIO client installed for storage operations:

```sh
python3 platform/ansible/scripts/qualify-storage-backup-destination.py \
  /etc/fire-fuse-storage/backup-destination.json \
  --mc /usr/local/bin/mc
```

The gate requires bucket access, fully enabled versioning without excluded
prefixes, default Object Lock retention in `COMPLIANCE` mode, and denial of
account-wide bucket enumeration to the backup credential. It does not upload
or mutate an object. Review the provider IAM policy separately because a
read-only probe cannot prove that every untested bucket and administrative API
is denied.

After reviewing the read-only evidence, run the controlled write qualification:

```sh
python3 \
  platform/ansible/scripts/qualify-storage-backup-destination-write.py \
  /etc/fire-fuse-storage/backup-destination.json \
  --mc /usr/local/bin/mc \
  --minimum-retention-days 14
```

This gate uploads one unique JSON marker beneath
`PREFIX/.qualification/`, reads its exact version back, checks its SHA-256,
requires inherited `COMPLIANCE` retention for at least the requested duration,
and requires version-specific deletion to fail. The locked marker cannot be
cleaned up immediately: its object key, version ID, checksum, and retention
deadline are the qualification evidence. Delete that exact version only after
the retention deadline and evidence review; never use wildcard cleanup.

`mc mirror` by itself is also insufficient as the only retained copy because it
synchronizes current objects without their version history and metadata. It
may be one transfer mechanism only when the destination independently creates
immutable point-in-time snapshots for each complete backup set.

## Initial retention target

Until workload size and recovery objectives are measured, use this conservative
staging baseline:

- 14 daily backup sets;
- 8 weekly backup sets;
- 12 monthly backup sets; and
- no automatic deletion of the final known-good set.

Retention begins only after a restore has succeeded from the selected
destination. Before that qualification, backup sets may be added but must not
be automatically expired.

The first retention implementation is planning-only. An operator selects the
transferred set ID from a successful restore rehearsal, and the planner
requires its canonical mode-`0600`, root-owned evidence file beneath
`/var/lib/fire-fuse/storage-backups/restores/`. A set ID alone is not restore
qualification.

Restore evidence has a strict version-1 shape. It binds the exact manifest and
transfer-record checksums, source and restored volume UUID, and remote prefix.
It also names an isolated rehearsal target and records successful manifest,
object snapshot, Redis snapshot, metadata load, JuiceFS status, clean-unmount,
mount-marker, remount-marker, and unchanged-production-guard checks.

The first-mount and remount marker SHA-256 values must match. The production
guard SHA-256 values captured before and after rehearsal must also match.
Every boolean check must be exactly `true`, the rehearsal must last from one
second through seven days, and unknown fields are rejected. The evidence
validator revalidates the complete local set and transfer record before
accepting any of these observations.

The evidence path is:

```text
/var/lib/fire-fuse/storage-backups/restores/SET-ID.json
```

This milestone defines and enforces the evidence contract but does not create
evidence manually; the isolated restore-rehearsal workflow generates it. The
end-to-end Ansible playbook only sequences the separately reviewable restore
roles and passes their generated rehearsal ID forward. It stops at the first
failed gate, retains the isolated target for diagnosis, and never performs
automatic cleanup.
Planning then requires a clean local audit and preserves:

- the newest transferred set as the final known-good set;
- the restore-qualified set;
- the newest set in each of the 14 most recent represented UTC days;
- the newest set in each of the 8 most recent represented ISO weeks;
- the newest set in each of the 12 most recent represented UTC months; and
- every set with any recorded remote object retention still active.

Incomplete and local-only captures are also kept outside automatic retention.
Only transferred sets outside every preservation rule and past all recorded
retention deadlines are labeled `candidate_expire_complete_set`. The planner
emits a deterministic dry-run report and has no delete, move, or plan-file
write path. A candidate is a review result, not deletion authorization.

Every retention operation must work from complete-set manifests. It must not
delete individual JuiceFS objects based only on their object age because old
objects can still be referenced by current metadata.

## Restore contract

Restore is always rehearsed into isolated targets:

1. Materialize the exact recorded off-host object versions into a new,
   root-only rehearsal directory and verify every checksum.
2. Restore its object snapshot into a new, empty bucket.
3. Restore the Redis snapshot into an isolated Redis instance for
   engine-specific recovery testing.
4. Separately load the JuiceFS metadata dump into an empty Redis database using
   the same pinned JuiceFS client.
5. Reapply the object-store endpoint and credential from the encrypted
   operations secret store.
6. Run `juicefs status` and require the expected volume identity and restored
   bucket.
7. Mount the restored volume on an isolated host, verify representative files,
   unmount, remount, and verify them again.
8. Record duration, errors, recovered backup timestamp, and evidence that the
   production volume was not modified.

The MinIO artifact is a raw archive of a stopped standalone MinIO data
directory, including `.minio.sys`. Extract it only into an empty data directory
for an isolated instance running the captured, pinned MinIO version. After
that instance is healthy, copy the captured workspace bucket into the new
empty recovery bucket used by the restored JuiceFS volume. Do not extract the
raw archive over any running or populated MinIO installation.

Loading a dump into a nonempty metadata database or restoring over the live
MinIO bucket is prohibited.

The materialization stage is deliberately separate from service restoration.
It requires a clean local audit and a valid transfer record, requalifies the
configured destination, and downloads `manifest.json` plus all four artifacts
using their recorded object version IDs. It refuses to reuse a rehearsal
directory and publishes `materialization.json` only after the complete
downloaded set verifies.

Before downloading begins, materialization captures a production
configuration guard from the trusted metadata URL file and `juicefs status`.
The guard contains only the metadata-URL SHA-256 and normalized volume name,
UUID, storage type, and bucket; it excludes the metadata URL itself,
credentials, sessions, and mutable usage counters. The captured identity must
match the materialized manifest before the root-only
`production-guard-before.json` record is published. Finalization must reproduce
the same digest. This proves that the restore workflow did not redirect or
reconfigure production storage; service-state guards and isolated endpoint
bindings provide the separate runtime boundary. It does not claim that normal
application writes to a live production volume did not occur.

This stage does not decrypt `restore-secrets.vault`, extract an archive, start
Redis or MinIO, load JuiceFS metadata, or touch production storage. A failed
attempt remains isolated beneath
`/var/lib/fire-fuse/storage-restores/staging/` for operator inspection and has
no materialization success record.

The next stage validates and extracts the raw MinIO tar into the rehearsal's
new `services/minio/data` directory. It accepts only regular files and
directories with clean relative paths, rejects duplicate paths, links, special
files, traversal, excessive member counts, and expansion beyond the raw tar
size, and requires both `.minio.sys/format.json` and the workspace bucket.
`minio-extraction.json` is published only after extraction succeeds. This
stage still does not start MinIO or contact production storage.

Before isolated MinIO starts, the extracted tree is hashed again and must
match its extraction evidence. The restore instance uses the captured MinIO
release and checksum-pinned installed binary, binds its API and console only
to loopback, and runs under the `minio` service account in a dedicated
systemd sandbox. Its root password is newly generated for the rehearsal and
stored only in the root-readable rehearsal environment file. Qualification
requires readiness and successful access to the captured workspace bucket;
the production MinIO service state must remain unchanged.

The captured bucket is never used directly by restored JuiceFS metadata. A
new rehearsal-specific recovery bucket is created in the isolated MinIO
instance and receives the current version of every captured object. The
workflow hashes every source object before copying, hashes the source again
afterward, and independently hashes the recovery bucket. Object names, sizes,
content hashes, counts, and aggregate bytes must match before
`minio-recovery-bucket.json` is published. Existing or partially populated
recovery buckets are never reused, overwritten, removed, or repaired
automatically.

Redis persistence is qualified separately from the metadata-dump restore. Its
gzip tar is extracted into a new rehearsal target with traversal, link,
special-file, duplicate-path, member-count, and expanded-size defenses. Both
`dump.rdb` and multi-part AOF persistence are required, and the extracted tree
is hashed again immediately before startup. A loopback-only, unprivileged
Redis service using the captured Redis release must load the persistence,
report healthy AOF state, and expose at least one key in the JuiceFS database.
The service uses a new rehearsal password, is not enabled at boot, and must
leave the production Redis service state unchanged.

The portable `metadata.dump` is then loaded into a different, newly created
empty Redis database using the checksum-pinned JuiceFS client and `--binary`.
After import, `juicefs config` replaces the omitted object-store credentials
and source bucket with the rehearsal MinIO credentials and qualified recovery
bucket. `juicefs status` must report the original volume name and UUID, S3
storage, the recovery bucket, and no sessions. This stage does not reuse or
modify the Redis persistence-validation instance.

The restored metadata is mounted only beneath `/mnt/fire-fuse-restores/`.
Qualification hashes a nonempty representative file, writes and flushes a
rehearsal marker, cleanly unmounts, and remounts with a second empty client
cache. Both the representative file and marker must retain their hashes. The
marker is then removed with `juicefs rmr --skip-trash`, the volume is flushed
and cleanly unmounted again, and the empty unmounted mountpoint is verified.
The production Redis and MinIO services and both isolated restore services are
state-guarded across the operation. This step requires at least one nonempty
file in the restored volume and never mounts or accesses `/workspace`.

Finalization consumes only root-owned, mode-`0600` qualification records. It
requires every record to identify the same rehearsal and backup set, checks
the original manifest and transfer-record hashes, and recaptures the stable
production configuration guard. Only an exact guard payload and SHA-256 match
can produce the canonical evidence beneath
`/var/lib/fire-fuse/storage-backups/restores/`. The existing strict validator
immediately revalidates the result against the original local backup set and
transfer record. If validation fails, the newly written evidence is removed,
so retention remains locked.

Once canonical evidence has been reviewed, rehearsal cleanup revalidates it
before any removal and requires the supplied set and rehearsal IDs to match.
It refuses a mounted rehearsal path, stops and removes only the three
rehearsal-specific services, and deletes only that rehearsal's staging tree,
mountpoint, and caches. Production service states are checked before and
afterward. The backup set and canonical restore evidence remain intact, and a
root-only cleanup record is retained under
`/var/lib/fire-fuse/storage-backups/restore-cleanups/`. Cleanup is retryable
after a partial attempt but refuses to overwrite a completed cleanup record.

## Verification schedule

- Verify checksums and manifest completeness after every backup.
- Perform an isolated metadata-load test monthly.
- Perform a complete object-and-metadata restore at least quarterly and before
  declaring the storage production-ready.
- Perform an additional restore after changing JuiceFS, Redis, MinIO, backup
  tooling, credentials, or retention rules.

The measured successful restore time becomes the evidence for an RTO. The time
between successful off-host backup sets becomes the evidence for an RPO. No RTO
or RPO is claimed before those measurements exist.

## Ownership boundary

Ansible or a standalone root-owned systemd service and timer may implement this
procedure after the destination is selected. fire-fuse must not receive backup
credentials, invoke backup tools, enforce retention, or restore storage.

The orchestrator's only involvement is its existing lifecycle behavior:
draining VMs, stopping cleanly, restarting, and reporting readiness.

## Upstream behavior relied upon

- [JuiceFS metadata backup and recovery](https://juicefs.com/docs/community/metadata_dump_load/)
  documents dump/load, binary dumps, omitted object-store secrets, and the lack
  of snapshot consistency during active writes.
- [JuiceFS status and maintenance](https://juicefs.com/docs/community/administration/status_check_and_maintenance/)
  documents that `juicefs status` reports all active client sessions.
- [MinIO `mc mirror`](https://min.io/docs/minio/linux/reference/minio-mc/mc-mirror.html)
  documents that mirroring does not preserve object versions or metadata.
- [MinIO object locking and immutability](https://docs.min.io/aistor/administration/object-locking-and-immutability/)
  documents version-level retention and COMPLIANCE-mode deletion protection.
