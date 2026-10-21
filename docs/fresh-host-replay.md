# Fresh compute-host replay

The control/build VM can reconstruct a **fresh** Fire Fuse host's product
projection from PostgreSQL and retained prepared images in R2. This is an
operator-controlled cold cutover. It does not move running Sandboxes, Session
events, accepted Messages, or Workspace bytes. Complete the Workspace storage
move or restore the existing JuiceFS volume before using it for host replacement.

## Preconditions

1. Stop public ingress and new control API mutations. Drain all accepted
   Messages and terminate Sandboxes on the old host. Record terminal or
   unavailable outcomes before discarding its SQLite state.
2. Stop `cantelop-control-worker.service` on the control/build VM. Settle all
   queued or running PostgreSQL operations and pending releases. The replay
   command refuses to proceed while any remain.
3. Provision and qualify the replacement host with a **new empty** Fire Fuse
   SQLite database, compatible boot artifacts and snapshots, the recovered
   Workspace volume, and the same private provider ingress over WireGuard.
   Verify `GET /v1/apps` on its loopback control API returns no Apps. Never
   point the replay command at an existing active host.
4. Retain every successful Build's prepared manifest and ext4 image in R2.
   The plan refuses gaps or conflicting host Build versions and refuses an App
   whose active release has no replayable Build. It selects the Build for the
   Console active release after replay, even when newer failed or rolled-back
   releases have retained Builds.

Run the checksum-pinned worker binary on the control/build VM with its normal
environment file. Dry-run first:

```sh
sudo systemctl stop cantelop-control-worker.service
sudo systemd-run --wait --collect --pipe \
  -p User=cantelop-control \
  -p EnvironmentFile=/etc/cantelop-control/worker.env \
  /usr/local/bin/cantelop-control-worker --replay-host
```

The dry-run prints only App, Workspace, and Build counts. After checking the
target and plan, apply once:

```sh
sudo systemd-run --wait --collect --pipe \
  -p User=cantelop-control \
  -p EnvironmentFile=/etc/cantelop-control/worker.env \
  /usr/local/bin/cantelop-control-worker --replay-host --apply
```

The tool registers active Apps, installs their retained Builds in host-version
order, explicitly selects the Build for each Console active release (or clears
the selection when none is active), projects current encrypted configuration,
then registers unarchived Workspaces. It uses the original Build operation IDs
and verifies returned Build IDs and versions. PostgreSQL is read-only during
replay. The Fire Fuse
host downloads and verifies each prepared ext4 image from a short-lived signed
R2 URL. No archive conversion runs on the compute host.

If replay fails, keep ingress stopped. Inspect the error and the partially
rebuilt target, then reset the **replacement** host to a fresh empty state
before retrying. Do not run the command against the old host. After success,
compare App and Workspace counts and active Build identities, run a
Workspace-backed Sandbox canary, then restart the normal control worker and
restore ingress. Keep the old host fenced until the new one is qualified.
