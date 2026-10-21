# Storage performance qualification

Performance limits must come from repeatable measurements on the dedicated KVM
host. They are not daemon defaults and are not inferred from development
machines.

The guest image contains `/usr/local/bin/fire-fuse-workspace-benchmark`. Its
`describe` command reports protocol version 2. Each measured command emits one
JSON object with:

- `schema_version`: result schema version;
- `workload`: stable workload name;
- `run_id`: caller-supplied correlation identifier;
- `operations`: files or I/O operations represented by the run;
- `bytes`: known payload bytes, or zero when content sizes vary;
- `duration_ns`: elapsed guest wall-clock nanoseconds.

The protocol provides these workloads:

- `small-files COUNT RUN_ID` creates, closes, and syncs individual files in a
  unique workspace directory. Directory creation and cleanup are excluded.
- `dependency-tree COUNT RUN_ID` builds a deterministic tar archive under
  `/tmp`, then measures extraction and sync into the workspace. This isolates
  the filesystem-heavy materialization phase of dependency installation from
  registry, DNS, and package-manager variability. Archive construction and
  cleanup are excluded. Extraction uses `--no-same-owner` because the
  Workspace NFS export does not permit guest root to restore archive ownership.
- `sequential-write SIZE_MIB RUN_ID` replaces a stable seed file using 1 MiB
  writes and `fsync`. The seed remains for cross-VM read tests.
- `sequential-read SIZE_MIB RUN_ID` verifies the seed size and reads it with
  1 MiB requests.
- `sequential-read-at SIZE_MIB RUN_ID START_UNIX_NS` performs the same
  validation before waiting for a shared Unix-nanosecond start time. It reports
  the scheduled and actual start plus their difference, allowing a driver to
  verify multi-VM start skew. The start must be within the next 60 seconds.
- `sequential-clean SIZE_MIB` removes and syncs the seed without emitting a
  measurement.

Every mutating workload first requires `/workspace` to be an actual mount.
Counts, sizes, and run IDs are bounded, and temporary paths use fixed prefixes.
The Ansible benchmark requires protocol version 2 and the
`sequential-read-at` capability.

The guest helper intentionally does not create or terminate VMs, alter the
host JuiceFS cache, classify reads as cold or warm, coordinate concurrency, or select
production thresholds. A host-side qualification driver must own those
operations and preserve the raw JSONL results together with the exact host,
artifact manifest, cache size, concurrency, and timestamp.

## Single-VM baseline

The opt-in Ansible baseline requires:

```yaml
fire_fuse_benchmark_confirm: true
fire_fuse_benchmark_trials: 3
fire_fuse_benchmark_ssh_key: /root/.ssh/id_ed25519
fire_fuse_benchmark_ssh_port: 22
fire_fuse_benchmark_runtime_port: 3000
fire_fuse_benchmark_output_directory: /absolute/controller/result/path
fire_fuse_benchmark_concurrency: 3
fire_fuse_benchmark_build_id: sha256:replace-with-active-catalog-build-id
# Optional; a failed valid policy makes the playbook fail after retaining its summary.
fire_fuse_benchmark_policy: /absolute/controller/path/policy.json
```

Run `platform/ansible/playbooks/compute/benchmark-storage.yml` only on a ready, drained
staging host. It performs 2-20 complete trials, defaulting to 3. Each trial
repeats preflight, uses unique run and workspace IDs, and stores the
invocation's shared series ID plus its one-based trial number. Controller
filenames include both values. A failure stops the series after cleanup instead
of treating partial output as success.

Within each trial, the role measures synchronous loopback create and terminate
API calls using curl's host-local request timer, then executes the small-file,
dependency-tree, sequential-write, and same-VM read workloads. It terminates
the writer, mounts the same workspace in a fresh VM, measures the first and
immediately repeated reads, and terminates the reader while retaining the seed
for the coordinated cohort described below. After that phase it removes the
seed and fetches a mode-0600 JSON result from the host.

The result records workload parameters, VM size, startup duration, graceful
shutdown duration, the complete request-scoped warmup trace, and the guest
protocol objects. The trace contains nanosecond start offsets and durations for
each host operation plus the guest assignment milestones for clock
synchronization, networking, image mount, workspace mount, workload start, and
SDK listener readiness. Reads are explicitly
classified `same-vm-after-write`, `new-vm-first-read`, and
`new-vm-immediate-repeat`. Replacing the VM does not reset the host JuiceFS
client, so the first new-VM read is marked `shared-client-cache-influenced` and
the repeat is only an `immediate-repeat-candidate`. Neither is a globally cold
measurement because host and backend caches remain intact.

After the cache comparison, the driver terminates that reader and creates a
fresh cohort of `fire_fuse_benchmark_concurrency` VMs. It launches protocol-v2
reads asynchronously against one start timestamp, rejects participants whose
start lag exceeds `fire_fuse_benchmark_maximum_start_lag_ms`, and records the
observed start skew, cohort wall duration, and aggregate bytes. The result
declares that this coordination assumes the Firecracker guests' wall clocks are
synchronized. Admission capacity must fit the complete cohort plus one
recovery VM. The combined host result uses schema version 5; its embedded guest
workload records remain schema version 2.

Immediately before every trial, the role verifies root ownership, modes, and
SHA-256 identities for the active orchestrator, systemd unit, configuration,
Firecracker, jailer, selected kernel, platform rootfs, selected container
rootfs, and all three snapshot bundle files. It requires the requested image ID
to exist in the active catalog, verifies the container rootfs against the
catalog checksum and size, and requires the requested vCPU and memory values to
match the snapshot manifest exactly.
After the first VM starts, it also hashes the guest benchmark helper. The role
requires the running service's `/proc` executable to match the active
orchestrator hash. The result records paths, hashes, and host file sizes.
Hashing the kernel and both root filesystems before each trial deliberately
standardizes integrity and host page-cache preconditioning; lifecycle timing
is therefore not a cold host-cache measurement.

An `always` cleanup removes asynchronous status files, retries seed removal,
terminates any live VM with the run's unique workspace ID, and requires
`/readyz`.

## Summarize repeated trials

The playbook automatically validates and summarizes its complete series into a
neighboring `HOST-SERIES_ID-summary.json` file. Without a policy, successful
summary validation is the final playbook gate. To reproduce that operation
manually for at least two result files with identical settings, run:

```sh
python3 platform/ansible/scripts/summarize-storage-benchmarks.py \
  --output /absolute/result/path/summary.json \
  /absolute/result/path/staging-1-series-ID-trial-*.json
```

The dependency-free controller tool accepts only schema-v5 snapshot host
results. It refuses duplicate run IDs or differences in host, profile, VM
size, workload parameters, image ID, artifact identity, clock model, metric
groups, or samples per trial. It also requires one series ID and the complete
contiguous trial sequence. Its
schema-version-1 summary reports min, median, nearest-rank p95, and max for
lifecycle timing, coordinated cohort timing and throughput, and each workload's
duration and rates. It additionally requires identical
host-step occurrence counts, including the snapshot assignment step, then
reports a distribution for every step's first start offset and per-trial
aggregate duration. Guest-side assignment progress appears as
`guest.assignment.wait_<event>` host steps. Concurrent participant samples remain
explicitly marked by their `samples_per_trial` count.

## Apply an acceptance policy

The generic benchmark playbook applies no acceptance policy by default. After
representative qualification establishes limits, create a separate JSON policy
using this shape, replacing angle-bracket placeholders with integers:

```json
{
  "schema_version": 1,
  "minimum_trials": <TRIALS_FROM_2_TO_20>,
  "checks": [
    {
      "name": "concurrent-throughput-median",
      "target": {
        "section": "coordination",
        "measurement": "aggregate_bytes_per_second"
      },
      "statistic": "median",
      "comparison": "at_least",
      "threshold": <MINIMUM_BYTES_PER_SECOND>
    }
  ]
}
```

Set `fire_fuse_benchmark_policy` to apply the policy during the playbook, or run
the same summarizer manually with `--policy /absolute/path/policy.json`. Lifecycle
and coordination targets require `section` and `measurement`. A `metric`
target must additionally provide the complete workload, cache scope, cache
state, cache guarantee, and concurrency selector so it resolves exactly one
metric group. Supported statistics are `min`, `median`, `p95_nearest_rank`,
and `max`; comparisons are `at_most` and `at_least`.

The summary embeds the canonical policy SHA-256, every observed value and
threshold, and the overall result. Exit status `0` means all checks passed,
`1` means the inputs or policy were invalid, and `2` means a valid policy
failed. A policy failure still writes the evidence file before the playbook
reports failure.

For the existing Hetzner 1-vCPU, 1024-MiB snapshot release gate, run
`platform/ansible/playbooks/compute/gate-vm-warmup.yml`. It measures ten snapshot pool
hits without running storage workloads and applies the tracked 260 ms median
and 300 ms nearest-rank p95 limits. The dedicated startup runner validates
the active image and snapshot artifacts, uses private ingress at `10.77.0.1:8084`
to register a temporary App and Workspace, and retains JSON evidence even if
the latency policy fails. Run `benchmark-storage.yml` separately for storage
throughput and filesystem workload measurements.

The four-vCPU snapshot requires a new measured warmup baseline and matching
release policy before this latency gate can qualify its deployment.

The guest workspace mount helper temporarily enables expedited RCU while
`mount.nfs4` runs. NFSv4 export traversal tears down temporary mounts, and
normal RCU grace periods can dominate this operation on multi-vCPU guests.
The helper restores the previous policy before reporting workspace readiness,
including after a failed mount or SIGINT/SIGTERM. Kernels without a writable
RCU control retain their normal mount behavior. Qualify changes with complete
startup traces and concurrent starts, rather than mount timing alone.

The snapshot builder also waits for the guest to close its readiness probe
connection before pausing the template. This prevents a live probe connection
from being captured alongside the guest listener. Check
`guest.assignment.connect_vsock` when qualifying a newly built snapshot;
assignment connection retries can otherwise obscure mount improvements.

## Preserve benchmark history

Raw benchmark results and qualification logs belong in ignored `dist/`
directories. After reviewing a completed schema-version-1 summary for
host-specific or sensitive data, copy the exact file into
`tests/benchmarks/history/` using `YYYY-MM-DD-HOST-SERIES_ID.json`. Do not edit old
summaries when the benchmark or artifacts change.

Report the tracked VM startup/warmup history with:

```sh
python3 scripts/report-vm-warmup.py
```

The report validates the stored trial count and startup distribution before
printing the history. See [`tests/benchmarks/README.md`](../tests/benchmarks/README.md) for
the retention convention and metric definition.
