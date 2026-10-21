# Benchmark history

This directory keeps the compact, comparable summaries from dedicated-host
qualification runs in source control. Raw results and qualification logs remain
under ignored `dist/` directories because they are much larger and may contain
host-specific operational detail.

VM warmup latency is the primary lifecycle metric. Here it means the host-local
wall time of the synchronous `POST /v1/sandboxes` request, ending only after
Firecracker starts and the guest workspace and workload readiness checks pass.
It is stored as `lifecycle.startup_duration_ns`.

Snapshot warmup qualification requires `host_pool.hit` and rejects
`host_pool.miss`. This keeps the primary series focused on the intended steady
state while the detailed trace and service logs retain pool-selection evidence.
The configured fixed capacity must cover the benchmark's concurrent reader
cohort; claimed capacity is replenished only after each sandbox terminates.

## Local dashboard

Run:

```sh
python3 scripts/benchmark-dashboard.py
```

and open <http://127.0.0.1:8321/>. The dashboard charts warmup latency
across the tracked series and, for summaries with detailed traces, breaks the warmup
window down into a host-step timeline (median start offset to median end
per step, grouped by subsystem) plus the guest boot milestones and the
per-workload storage metrics. History files are re-read on every refresh,
so a newly recorded series appears without restarting the server. The
server validates each file with the same parser as
`scripts/report-vm-warmup.py`; files it rejects are counted in the page
header instead of being charted.

## Current history

Run:

```sh
python3 scripts/report-vm-warmup.py
```

The report validates every `history/*.json` file and prints min, median,
nearest-rank p95, and max startup latency. A p95 from only a few trials is
directional; use more trials before adopting it as a service objective.
For benchmark summaries with detailed traces, it also prints the latest host steps ordered
by median duration. Cold-boot-era summaries also carry guest milestones, which
are printed ordered by boot uptime. Repeated
host operations are aggregated within each trial before their distributions
are calculated. Schema-v5 summaries also identify the selected catalog image;
historical rows without that field are labeled `unrecorded`.

The initial 2026-07-29 baseline contains three 1-vCPU, 512-MiB Ubuntu 24 trials.
Its median is 3.023 seconds and its observed maximum is 3.097 seconds. That
older series predates detailed trace retention, so it has no
per-operation or guest-milestone history.

The historical `cold-request-execution-lifecycle` summaries measured the
retired Executer path. They remain immutable comparison evidence, but the
runner and fixture were removed with the Message hard cutover. A future Message
benchmark must measure durable acceptance, handler settlement, and runtime
quiescence as separate milestones.

## Recording a qualification

1. Run the dedicated-host benchmark described in
   [`docs/storage-performance.md`](../../docs/storage-performance.md).
2. Produce its schema-version-1 summary with
   `platform/ansible/scripts/summarize-storage-benchmarks.py`.
3. Inspect the summary for secrets or inappropriate host identifiers.
4. Copy the exact summary into `tests/benchmarks/history/` using
   `YYYY-MM-DD-HOST-SERIES_ID.json`.
5. Run the warmup report and the script unit tests before committing:

   ```sh
   python3 scripts/report-vm-warmup.py
   python3 -m unittest discover -s scripts/tests -p 'test_*.py'
   ```

Do not rewrite old summaries when benchmark code or artifact identities change.
Each summary carries the compatibility and artifact data needed to decide
whether two runs can be compared. Keep acceptance policies separate until a
representative trial count establishes a defensible regression threshold.

## Session handler activation benchmark

The [2026-10-13 SDK 1.0 prepared-Workspace rerun](session-activation/history/2026-10-13-sdk1-prepared-under-500ms.json)
reached handler entry in 426 ms median and 484 ms p95/max across ten trials. All
ten met the original 500 ms target. The original SDK 0.17 fixture on the current
platform measured 440 ms median, with one 2,943 ms outlier caused by a slow
reconciliation pass for a stopped Sandbox delaying unrelated new work. Dispatcher
passes now retain their 16-worker limit and per-Sandbox serialization while
allowing new Sandboxes to proceed independently.

First-time Workspace creation was outside the original activation benchmark.
The separate SDK 1.0 fresh-Workspace cohort measured 3,463 ms median / 3,701 ms
maximum before changes (three pilot trials). Deferring unused application
database provisioning reduced ten-trial handler entry to 1,089 ms median /
1,267 ms maximum. Batching canonical registration reads then reduced it to
993 ms median / 1,185 ms maximum. These fresh Workspace timings include creation,
registration, Sandbox startup and actual handler entry; they do not meet a 500 ms
first-creation target. The worker host's measured SQL round trip was 103 ms.
SDK 1.0 databases provision on first authorized use; legacy runtime registration
continues to provision eagerly. A separate first-use smoke test verified that a
registered SDK 1.0 Workspace had no provider database, then successfully ran
`SELECT 1` through the ordinary SDK database API. All temporary trial Workspaces
were archived and purged through the canonical lifecycle.

The [2026-10-06 workspace-open fast-path trial](session-activation/history/2026-10-06-workspace-open-fast-path.json)
changes only the gateway to resolve registered active Workspaces locally. Missing
or archived Workspaces retain Console provisioning and registration waiting.
Ten fresh Sessions reached handler entry in 434 ms median and 458 ms p95/max,
versus 569 ms / 667 ms with fresh delivery. All ten samples were below 500 ms.
Submission-to-enqueue entry fell from 192 ms to 49 ms median. Core enqueue HTTP
completion, after durable commit, was reached in 66 ms median. Sandbox warmup
was 226 ms and readiness-to-handler 123 ms. Core, fresh-delivery dispatcher,
fixture Build and SDK 0.17.0 were unchanged. All trials completed on their first
attempt and fixture Sandboxes were terminated. These are ten sequential trials,
not a concurrent-load guarantee; nearest-rank p95 equals maximum.

**Acceptance boundary correction:** `accepted_at` is assigned at entry to the
enqueue service, before its SQLite transaction commits. Historical
`submitted_to_accepted` measures that timestamp; it does not include enqueue
persistence. `accepted_to_transport_claim` includes persistence time. This trial
also records `submitted_to_enqueue_response_logged`, ending at the core HTTP
handler completion after commit and response serialization. Total handler entry
continues to measure the unchanged submission-to-handler interval.

The [2026-10-06 fresh-delivery trial](session-activation/history/2026-10-06-fresh-delivery.json)
changes only the dispatcher to send a locally proven fresh execution before
ordinary reconciliation. Ten fresh Sessions reached handler entry in 569 ms
median and 667 ms p95/max, versus 599 ms / 831 ms after admission wake-up.
Readiness-to-handler fell from 153 ms to 120 ms median; initial inspection to
runtime acceptance fell from 70 ms to 40 ms. The observed gain is about 30 ms,
smaller than the earlier estimate. Submission-to-acceptance was 192 ms median,
Sandbox warmup 226 ms, and acceptance-to-handler 373 ms. Response latency fell
from 2,014 ms to 1,004 ms median as settlement was observed in the same pass.
All deliveries completed on the first attempt and all fixture Sandboxes were
terminated. Core and gateway binaries were unchanged during this trial; fixture
Build and SDK 0.17.0 were unchanged. The baseline preceded the merge deployment,
so its binary identities differ. Ten sequential trials remain diagnostic
evidence, not a concurrent-load benchmark; p95 equals maximum.

The [2026-10-06 admission wake-up trial](session-activation/history/2026-10-06-admission-wake.json)
updates core and dispatcher so successful durable enqueue wakes idle admission
workers and available messages are claimed without timer pacing. Ten fresh
Sessions reached handler entry in 599 ms median and 831 ms p95/max, versus
948 ms and 1,214 ms after the reconciliation wake-up. Acceptance-to-claim fell
from 304 ms median / 651 ms maximum to 19 ms / 37 ms. Acceptance-to-startup
fell from 325 ms to 29 ms median. Warmup stayed similar at 219 ms, while
readiness-to-handler was 153 ms. Submission-to-acceptance remained 189 ms
median; acceptance-to-handler was 403 ms median. All trials completed on their
first attempt and all fixture Sandboxes were terminated. Gateway binary,
fixture Build and SDK 0.17.0 were unchanged. These ten sequential trials are
diagnostic evidence, not a concurrent-load benchmark; p95 equals maximum.

The [2026-10-06 post-merge activation rerun](session-activation/history/2026-10-06-post-merge-activation.json)
records ten fresh Sessions against merged dispatcher release `ce0a698ccde0`.
Handler entry was 923 ms median / 1,711 ms p95/max, sandbox warmup was
230 ms median, and readiness to handler was 152 ms median / 158 ms maximum.
Acceptance to startup was 312 ms median / 844 ms maximum. The slowest trial
spent 825 ms waiting for transport claim; its readiness-to-handler interval
remained 153 ms. All trials completed on their first attempt and all fixture
Sandboxes were terminated. The artifact retains individual trials, milestone
distributions, deployed binary checksums, runner source revision, and the previous
trial comparison. Ten sequential samples are diagnostic evidence; p95 equals maximum.

The [2026-10-06 immediate reconciliation trial](session-activation/history/2026-10-06-immediate-reconciliation.json)
changes only the coordinator to wake its reconciliation loop after a new execution
has been persisted. Ten fresh Sessions reached handler entry in 948 ms median,
versus 1,163 ms before, and 1,214 ms p95/max versus 2,095 ms. Sandbox readiness to
handler entry fell from 653 ms median / 1,165 ms maximum to 149 ms / 161 ms. The
interval after the first runtime inspection fell from 570 ms to 69 ms median.
Capability, status-before-send and ambiguous-retry checks remain in place.

Acceptance-to-startup was higher in this cohort (325 ms median versus 213 ms),
so the total improvement was smaller than the reduction in delivery handoff.
Sandbox warmup was similar at 225 ms versus 219 ms. Each trial completed on its
first attempt and all fixture Sandboxes were terminated. Core, gateway, Console,
SDK 0.17.0 and fixture Build were unchanged. This is diagnostic evidence from ten
sequential Sessions, not a concurrent-load benchmark; p95 equals the maximum.

SDK 1.0 is the only supported SDK for new runs. Historical results retain their
original SDK versions for comparison.

`session-activation/` is a dedicated SDK App fixture for measuring when an agent
can actually start working. Its handler records its entry time and immediately
replies. Each trial uses a new logical Session with zero keep-alive and explicitly
stops it after collecting the reply. The SDK 1.0 timed request includes Workspace resolution, Sandbox startup and
Workspace mounting, durable transport admission, and runtime intake before
handler entry. The file-only handler does not request database credentials.
Workspace/database provisioning runs once through `/webhooks/prepare`, outside the trials.
Use `--fresh-workspaces` to include first-time Workspace readiness in every trial;
that cohort creates one Workspace per trial and must be reported separately. The
SDK 1.0 fixture has no model or authentication work. `--route-prefix ''` supports
rerunning historical fixtures with unprefixed routes. The runner requires the
actual deployed `--sdk-version` instead of assuming a version.

Build and deploy this fixture as a **dedicated benchmark App**, with a random secret
`BENCHMARK_TOKEN` of at least 32 characters. Store that token in a private local
file; never put it in command arguments. Then run:

```sh
python3 scripts/benchmark-session-activation.py \
  --origin https://YOUR-BENCHMARK-APP.cantelop.dev \
  --token-file /absolute/private/benchmark.token \
  --output /absolute/path/activation-summary.json \
  --sdk-version 1.0.0 \
  --platform-revision DEPLOYED-PLATFORM-COMMIT \
  --build-id sha256:FIXTURE-BUILD-DIGEST \
  --confirm-dedicated-app
```

The summary reports handler entry, reply response and client round-trip p50/p95.
Handler entry uses Edge/guest wall clocks and requires synchronized clocks; invalid
windows fail qualification. Response and client round-trip use monotonic clocks.
Client round-trip additionally includes transport and explicit Session cleanup.
The fixture excludes Claude CLI startup, authentication and model inference.
Keep its evidence separate from VM-only warmup history. Record the deployed
platform release and fixture Build alongside each summary for comparisons.

Coordinator logs now include `Session activation trace completed`, with stage
offsets/durations for opening the Sandbox, acquiring its lease, granting ownership
and resuming the mailbox. `resumed` identifies successful resume acknowledgments;
that acknowledgment does not itself prove handler entry. Correlate the trace with
the fixture's handler milestone using its `benchmark:<trial_id>` Session ID.

## Hetzner snapshot warmup release gate

`tests/benchmarks/policies/hetzner-snapshot-warmup-v1.json` is the release policy
for the current 1-vCPU, 1024-MiB snapshot profile on the dedicated Hetzner
reference host. It requires ten comparable trials, a warmup median no greater
than 260 ms, and nearest-rank p95 no greater than 300 ms. Warmup is the complete
synchronous create request through workspace and workload readiness.

Run the gate only against a ready, drained reference host:

```sh
ansible-playbook \
  --inventory inventory/staging/hosts.yml \
  playbooks/compute/gate-vm-warmup.yml
```

The gate runs a dedicated startup-only runner. It registers a temporary App
and Workspace through the private WireGuard ingress, then measures ten
synchronous Sandbox creates through guest readiness. Every sample must be a
snapshot pool hit. It hashes the active orchestrator, selected image, kernel,
platform rootfs, and snapshot files before timing, and verifies the 1-vCPU,
1024-MiB snapshot shape. It terminates each Sandbox and archives the App.
Storage workloads run separately through `benchmark-storage.yml`; their
results cannot prevent startup latency from being measured. The gate fetches
its JSON evidence to `fire_fuse_benchmark_output_directory`, including when a
threshold fails. Startup-only evidence has its own schema and is not accepted
by the historical storage-summary report.

## Five-axis host capacity benchmark

`platform/ansible/playbooks/compute/benchmark-host-capacity.yml` runs an opt-in,
single-trial benchmark on a ready, drained compute host. It reads the active
Sandbox limit and NFS thread count without changing them. Run it only after
selecting a representative workload profile and a catalog Build whose guest
supports SSH and Python 3. The versioned
`profiles/host-capacity-example-v1.json` is an **example**, not an acceptance
policy; copy it and set the VM count and active workload counts before use.

Supply the following Ansible variables, preferably in a local file outside
source control:

```yaml
fire_fuse_capacity_confirm: true
fire_fuse_capacity_profile: /absolute/controller/path/profile.json
fire_fuse_capacity_build_id: sha256:replace-with-active-catalog-build-id
fire_fuse_capacity_ssh_key: /absolute/host/path/guest-test-key
fire_fuse_capacity_output_directory: /absolute/controller/path/dist/capacity
```

Then run:

```sh
ansible-playbook --inventory platform/ansible/inventory/staging/hosts.yml \
  --extra-vars @/absolute/controller/path/capacity-vars.yml \
  platform/ansible/playbooks/compute/benchmark-host-capacity.yml
```

The profile fixes the target cohort, startup concurrency, numbers of VMs doing
CPU, memory and Workspace work, workload sizes and durations, and host memory
safety limits. The runner requires the full cohort to be **running**, uses at
most 25 Workspaces per test App, and cleans up only its uniquely named test
Sandboxes, Workspaces (records and data) and Apps. A failed Sandbox retains its slot until deleted; cleanup
deletes failed records too. A guard trips on low available memory, unexpected
swap use or an OOM kill. Do not use this benchmark against a host with existing
active Sandboxes.

Each trial produces a private raw JSON file with host samples and a compact
`-summary.json` file. The five `axes` are:

1. CPU load: active VMs, mean host cores used, CPU pressure and throttling.
2. Memory load: touched MiB per VM, service resident memory, available memory,
   memory pressure, swap and OOM evidence.
3. Workspace I/O: separate guest write, `fsync` and read distributions,
   throughput, SSH round-trip timing and the NFS `sockets-enqueued` delta
   (requests that found no idle nfsd thread). The read
   advises the guest to discard cached file pages first; it is a best-effort
   NFS read probe, not a guarantee of a cold server-side cache.
4. Startup throughput: running VMs per minute and total cohort startup time.
5. Startup time: per-VM synchronous create-request p50, p95 and p99.

Results also record the workload profile, runner digest, host, active release,
snapshot, Build, guest shape, configured slots and NFS thread count. Use the
same profile and Build when comparing attempts. The benchmark does not define
an acceptance threshold for a workload we have not qualified. To compare two
summaries and optionally fail on a chosen regression percentage:

```sh
python3 scripts/compare-host-capacity.py BASELINE-summary.json CANDIDATE-summary.json
python3 scripts/compare-host-capacity.py --max-regression-percent 20 \
  BASELINE-summary.json CANDIDATE-summary.json
```

Review a summary for host-specific detail before copying it into
`tests/benchmarks/history/`. Keep raw sample files in ignored `dist/` storage.
