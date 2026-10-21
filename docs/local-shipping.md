# Ship host software from this machine

Use the local shipping commands for compatible software updates to provisioned
hosts. Fire Fuse, the builder, and storage are separate surfaces. Console and
Cloudflare deployments continue through their existing GitHub integrations.
Protocol cutovers, schema changes, credentials, host configuration, and base
image changes use the coordinated [deployment runbook](./deployment.md).

## Configure the controller once

The controller needs Python 3.12+, Ansible, Go, and SSH access. Builder
components need Bun 1.3.10, and the control worker also needs Docker for its
Linux checks; storage builds use Docker on macOS. Each component is built by its
surface's canonical release script (`scripts/build-compute-linux-amd64.sh`,
`scripts/build-builder-linux-amd64.sh`, or the storage build scripts). Existing
Go compilation/module caches are reused. The commands never fetch Git or merge
branches: select a locally available committed revision with `--ref` (default:
`HEAD`). Builds, playbooks, roles, units, and the host helper all come from
that committed snapshot; uncommitted files are excluded.

```sh
make ship-configure SHIP_ARGS="--inventory /absolute/path/to/hosts.yml --vault-password-file /absolute/path/to/vault-password"
```

This saves only the paths you pass, never credentials or defaults, in
`~/.config/cantelop/ship.json`, after checking that the effective inventory and
vault password file exist. An existing
Ansible vault password executable is also supported. By default the controller
uses this checkout's staging inventory and
`~/.config/cantelop/staging/ansible-vault-password`. Override configuration with
`--config`, individual path flags, or `CANTELOP_SHIP_INVENTORY`,
`CANTELOP_SHIP_VAULT_PASSWORD_FILE`, and `CANTELOP_SHIP_CACHE_DIR`.

## Select and ship one component

| Command | Default component | Other `--component` selections |
| --- | --- | --- |
| `make ship-fire-fuse` | Runtime daemon | `gateway`, `dispatcher`, `observer` |
| `make ship-builder` | Control worker and its migrations | `materializer` |
| `make ship-storage SHIP_ARGS="--component minio"` | Explicit selection required | `minio`, `juicefs` |

```sh
make ship-fire-fuse SHIP_ARGS="--ref origin/main --plan"
make ship-fire-fuse SHIP_ARGS="--ref origin/main --prepare"
make ship-fire-fuse SHIP_ARGS="--ref origin/main --preflight"
make ship-fire-fuse SHIP_ARGS="--ref origin/main"
make ship-builder SHIP_ARGS="--component materializer --ref origin/main"
make ship-storage SHIP_ARGS="--component juicefs --ref origin/main"
```

`--plan` prints scope without building or contacting hosts. `--prepare` runs
checks and builds locally without contacting hosts. `--preflight` additionally
checks hosts and stages binaries in an isolated directory; it never stops,
starts, or replaces a serving component. The worker uses its dedicated role's
check mode for this option. A normal run verifies vault
and SSH access first, then checks/builds the selected component, stages it,
preflights the hosts, applies once, and verifies readiness. It never invokes
the full `compute.yml`, `builder.yml`, or `storage.yml` provisioning playbooks.

Review the changes since the selected component's deployed revision. Changes
to shared contracts can require shipping multiple components or using the
coordinated runbook. Runtime shipping requires the installed and candidate
SQLite schema versions to match. It does not install kernels, Firecracker,
rootfs images, units, configuration, credentials, or migrations.

The builder worker reuses its existing dedicated role, including serialized
PostgreSQL migrations, connection checks, and authenticated compute-ingress
qualification. It does not update Docker or the build materializer.

Storage has no Cantelop-authored daemon: MinIO and JuiceFS are separate pinned
builds. Redis package/configuration updates remain provisioning operations.
JuiceFS shipping updates the storage administrative client and the compute
client/mount processes with the same checked binary. It does not format a
volume or change Redis, MinIO credentials, exports, or firewall rules.

## What happens during activation

Runtime and storage updates fence the existing Tunnel and new provider-ingress
connections (TCP 8084, used by the control worker over WireGuard), wait up to
120 seconds for accepted work, live sandboxes, leases, and materializations to
drain, and stop Fire Fuse. Stopping Fire Fuse ends running microVMs, so sessions
kept warm by keep-alive must end first; failed sandboxes do not block the drain.
Set `drain_seconds` in the machine configuration to a value between 1 and 3600
to change that bound. A drain timeout replaces no binaries and restores the
recorded services. Gateway, dispatcher, observer, and builder updates restart
only their selected processes. A materializer update first closes its socket to
the worker and waits, within the same bound, for in-flight conversions.

A restarted component passes readiness only through its endpoint, its socket,
or its JuiceFS mounts, retried for up to 30 attempts. A component without such a
probe must stay active across consecutive checks, and any automatic systemd
restart during readiness fails the candidate.

The narrow host helper retains the previous binary before replacing it. Failed binary
activation restores that binary and checks it. Runtime updates additionally
retain a database backup; automatic recovery never restores that backup over
live data. The controller restores the previously active compute services and
Tunnel only after runtime readiness passes. If restoration fails, ingress
stays fenced and the command fails with its evidence path.
The builder worker retains its existing migration and restart procedure and
records its source/artifact identity after the dedicated role succeeds.

For controller interruption after fencing, recover using that run's generated
variables and the same inventory/vault arguments:

```sh
cd ~/.cache/cantelop/ship/sources/<commit>/platform/ansible
ansible-playbook --inventory /absolute/path/to/hosts.yml \
  --vault-password-file /absolute/path/to/vault-password \
  --extra-vars @/absolute/path/to/run/ansible-vars.json \
  --extra-vars '{"ship_action":"resume"}' playbooks/ship/boundary.yml
```

## Cache and evidence

`~/.cache/cantelop/ship` contains source snapshots (the three most recently
used are kept), component artifacts, storage build caches, and private run logs.
Artifacts are kept because the inventory can pin them.
Go uses its normal persistent caches. A checked artifact can be reused for the
same component, source commit, and controller implementation only when every
recorded SHA-256 still matches. Different committed revisions rerun checks.

Each run prints its private evidence directory containing step logs,
argument-free timings, and generated Ansible artifact variables. No secret
values are copied into machine settings or generated variables. Hosts retain
the source commit and installed checksum of every shipment under
`/var/lib/cantelop/local-ship`; previous binaries, runtime database backups, and
staged artifacts are kept for the three most recent runs. An unchanged installed
binary is not restarted.
If all selected binaries already match, the controller skips activation and
the maintenance boundary entirely. Releases are serialized on this controller;
remote operations also take a host lock. Measure the resulting timings before
adding further optimizations.

## Keep provisioning in step

Shipping does not edit the inventory. After a successful run, pin the printed
artifact directory's files and checksums in the inventory variables for that
component. Until then, `compute.yml`, `builder.yml`, and `storage.yml` refuse to
replace a locally shipped binary that is still installed. Pass
`-e local_ship_allow_replace=true` to replace it intentionally. The runtime is
excluded because only the coordinated release checkpoints install it.
