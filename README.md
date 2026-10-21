```text
 ██████╗ █████╗ ███╗   ██╗████████╗███████╗██╗      ██████╗ ██████╗
██╔════╝██╔══██╗████╗  ██║╚══██╔══╝██╔════╝██║     ██╔═══██╗██╔══██╗
██║     ███████║██╔██╗ ██║   ██║   █████╗  ██║     ██║   ██║██████╔╝
██║     ██╔══██║██║╚██╗██║   ██║   ██╔══╝  ██║     ██║   ██║██╔═══╝
╚██████╗██║  ██║██║ ╚████║   ██║   ███████╗███████╗╚██████╔╝██║
 ╚═════╝╚═╝  ╚═╝╚═╝  ╚═══╝   ╚═╝   ╚══════╝╚══════╝ ╚═════╝ ╚═╝
```

Cantelop is a platform for running isolated Sessions of Apps on Firecracker
microVMs. Each compute host is run by `fire-fuse`, a deliberately small
orchestrator with a narrow HTTP API for starting, inspecting, and terminating
Sessions without introducing a cluster scheduler.

The source tree and its dependency boundaries are described in
[docs/architecture/repository-layout.md](docs/architecture/repository-layout.md).

SDK **1.0** is the only supported SDK. Install the stable package with
`npm install @cantelop/sdk`; older SDKs and 1.0 prereleases are unsupported.
Start with the [quick start](https://cantelop.com/docs/quick-start), and see
[the platform contract](docs/sdk-1-platform.md) for implementation details.

Cantelop-authored platform code is **AGPL-3.0-only**, with MIT-licensed starters
and the exceptions in the [license map](docs/licensing.md). The separate SDK
remains MIT licensed. See [third-party notices](THIRD_PARTY_NOTICES.md),
[contribution instructions](CONTRIBUTING.md), and [security reporting](SECURITY.md).
This is a **public source preview**. Supported self-hosting remains in progress
under the [open-source release plan](docs/open-source-release-plan.md).
Public development and contributions live in
[`stepandel/cantelop`](https://github.com/stepandel/cantelop). Hosted production
deployments remain in a separate private repository. See
[source publication](docs/source-publication.md) for the review and promotion process.

The current endpoint, error, lifecycle-state, durability, restart, and shutdown
contract is documented in [docs/api-lifecycle.md](docs/api-lifecycle.md), with
machine-readable fire-fuse request and response schemas in
[docs/openapi.yaml](docs/openapi.yaml); the console tenant API used by the CLI
is in [docs/openapi-console.yaml](docs/openapi-console.yaml). Manual host setup, startup, monitoring,
shutdown, and recovery procedures are in
[docs/operations.md](docs/operations.md). A hardened service unit and install
instructions are in [docs/systemd.md](docs/systemd.md). The storage benchmark
protocol and measurement boundaries are in
[docs/storage-performance.md](docs/storage-performance.md). Docker image
materialization, Sandbox semantics, and artifact lifecycle are in
[docs/docker-images.md](docs/docker-images.md). App hostname routing and cold
Session activation are in [docs/gateway.md](docs/gateway.md).
Customer login, App creation, and direct Docker image deployment are in
[docs/cli.md](docs/cli.md).
The clean-slate separation between the PostgreSQL-backed product control plane
and the fire-fuse compute agent is in
[docs/external-control-plane.md](docs/external-control-plane.md).
The customer management and observability console and its authenticated host
observer are documented in [docs/dashboard.md](docs/dashboard.md).
The canonical release and deployment order, including GitHub Actions,
Message-era host rollout, canary qualification, and rollback, is in
[docs/deployment.md](docs/deployment.md).

## Components

Cantelop runs on three hosts (compute, storage, and builder) plus Cloudflare's
edge:

- **Compute host:** the `fire-fuse` runtime supervises Firecracker microVMs,
  Sessions, Sandboxes, TAP networking, and Workspace mounts behind a small,
  idempotent HTTP API; the gateway accepts authenticated edge control and
  Messages; the dispatcher delivers Messages to durable Session actors; and
  `fire-fuse-observer` exposes authenticated host observability.
- **Storage host:** Redis, MinIO, and the JuiceFS volume that back Workspaces.
- **Builder host:** the Console control worker and the root-owned image
  materializer that converts Docker archives into Fire Fuse images.
- **Console:** the Next.js product control plane and management UI, backed by
  PostgreSQL.
- **Edge:** Cloudflare Workers (dispatcher, outbound, origin broker, and tail)
  that route App traffic and broker calls back to the platform.
- **Clients:** the `cantelop` CLI, which builds and deploys projects that use
  the `@cantelop/sdk` package.

## Run locally

```sh
cp config.example.json config.local.json
# Edit config.local.json to point at this host's binaries and boot images.
sudo install -d -m 0750 /etc/fire-fuse
sudo install -m 0600 config.local.json /etc/fire-fuse/config.json
sudo go run ./fire-fuse/runtime/cmd/fire-fuse -config /etc/fire-fuse/config.json
```

## Real Firecracker integration tests

A reproducible Hetzner dedicated-server staging deployment, including Robot API
helpers, a host installer, systemd configuration, and a guest-reachability
qualification run, is documented in
[platform/hetzner/README.md](platform/hetzner/README.md).

The real-host integration suite is disabled during ordinary `go test ./...` runs because
it requires Linux, `/dev/kvm`, root networking privileges, Firecracker and
jailer binaries, and compatible kernel/rootfs fixtures. A dedicated integration
run enables it explicitly; after that, missing prerequisites fail rather than
skip:

```sh
sudo --preserve-env=PATH env \
  FIRE_FUSE_INTEGRATION=1 \
  FIRE_FUSE_INTEGRATION_CONFIG=/etc/fire-fuse/integration-config.json \
  FIRE_FUSE_INTEGRATION_BUILD_ID=sha256:replace-with-catalog-build-id \
  go test -v ./tests/integration
```

Install the integration configuration as a root-owned, non-writable regular
file beneath a trusted hierarchy before running this root-level integration suite.

The integration configuration uses the normal strict schema to select binaries,
jailer identity, networking, the default snapshot, and the content-addressed
image catalog. The Build ID is explicit so the same suite works with multiple
catalog entries. The integration suite verifies that the default profile's snapshot
manifest is exactly 4 vCPUs / 512 MiB boot memory plus 4096 MiB hotplug, replaces its runtime and SQLite paths
with a short isolated directory beneath `/var`, checks the complete host
preflight contract, and removes the directory afterward. Its lifecycle test
starts that exact snapshot shape through the production supervisor and verifies
its durable process, jail, TAP,
and firewall ownership, terminates it, and checks that no owned host resources
remain.
The restart test boots the same VM shape in a helper process that exits without
running cleanup, then verifies that a fresh supervisor safely terminates the
recorded PID and releases every persisted resource through startup recovery.
A process-crash test kills Firecracker through a Linux pidfd, verifies that the
background watcher durably records `failed/runtime_exited`, and then checks that
terminating the failed record releases all remaining ownership.
Set `FIRE_FUSE_INTEGRATION_GUEST_TCP_PORT` to make the lifecycle test also wait
for a TCP service started by the guest image. The Hetzner staging runner requires
this additional application-level probe after the snapshot assignment reports
that the workload has started.

The server listens on `127.0.0.1:8080` by default. Operator calls require the
root-only token from `/etc/fire-fuse/control-clients/operator.token`; gateway,
dispatcher, and observer calls use separate route-scoped credentials. The server
still restricting `-listen` to a numeric IPv4 or IPv6 loopback address with a
nonzero port. Hostnames, wildcard addresses, and non-loopback interfaces are
rejected before fire-fuse acquires host resources. Its liveness endpoint also
requires that token:

```sh
curl --header "Authorization: Bearer $(sudo cat /etc/fire-fuse/control-clients/operator.token)" \
  http://127.0.0.1:8080/healthz
```

Every API response includes `X-Request-ID`. A caller-supplied identifier is
preserved only when it is 1–64 ASCII letters, digits, dots, underscores, or
hyphens and begins with a letter or digit; otherwise fire-fuse generates one.
The executable emits JSON logs and records one completion event per request
with its request ID, method, matched route, status, duration,
and a client/server error class for 4xx/5xx responses. Raw URLs, query strings,
request bodies, and request headers are not logged.

`/healthz` only reports that the HTTP process is alive. `/readyz` reruns host
preflight checks and returns `200 OK` only while the host can accept lifecycle
work:

```sh
curl http://127.0.0.1:8080/readyz
```

Apps and their persistent Workspaces are registered from the external product
control plane through the private compute-provider contract. The loopback API
reads provider-local App and Workspace projections and starts Sessions:

```sh
fire_fuse_token=$(sudo cat /etc/fire-fuse/control-clients/operator.token)
curl --header "Authorization: Bearer ${fire_fuse_token}" \
  http://127.0.0.1:8080/v1/apps/editor
curl --header "Authorization: Bearer ${fire_fuse_token}" \
  'http://127.0.0.1:8080/v1/workspaces?app_id=editor'

curl --request POST http://127.0.0.1:8080/v1/sessions \
  --header "Authorization: Bearer ${fire_fuse_token}" \
  --header 'Content-Type: application/json' \
  --data '{"id":"ses_editor_01","workspace_id":"wsp_editor","keep_alive_seconds":0}'
```

The Session ID is the idempotency boundary. Retrying the same ID and
Workspace returns the existing Session; reusing it for another Workspace
returns `session_conflict`. Each activation derives its Workspace and Session runtime
from the Workspace and App and resolves the Workspace's current Build. On
success the API returns `202 Accepted` with an `active` Session.

The loopback App collection has no creation method. Its resource endpoints are
read-only feeds for trusted host components; explicit-owner provisioning is
isolated under the internal admin namespace.
Inspect Apps, Workspaces, or Sessions:

```sh
curl --header "Authorization: Bearer ${fire_fuse_token}" http://127.0.0.1:8080/v1/apps
curl --header "Authorization: Bearer ${fire_fuse_token}" \
  'http://127.0.0.1:8080/v1/workspaces?app_id=editor'
curl --header "Authorization: Bearer ${fire_fuse_token}" \
  'http://127.0.0.1:8080/v1/sessions?workspace_id=wsp_editor'
curl --header "Authorization: Bearer ${fire_fuse_token}" \
  http://127.0.0.1:8080/v1/sessions/ses_editor_01
```

The lifecycle service watches each tracked Firecracker process and changes its
stored state from `running` to `failed` as soon as it exits. Inspection also
rechecks liveness as a safety net. The jail remains owned by that record until
DELETE or service shutdown cleans it up.

Failed records include a stable `failure.reason`. Startup failures use
`runtime_start_failed`; unexpected exits use `runtime_exited` and include the
runtime PID plus either its exit code or terminating signal. Raw process errors
are not exposed.

Terminate a record by ID:

```sh
curl --request DELETE \
  --header "Authorization: Bearer ${fire_fuse_token}" \
  http://127.0.0.1:8080/v1/sessions/ses_editor_01
```

Release is idempotent. It transitions the backing Sandbox through `stopping`,
asks the guest to shut down through Firecracker's `Ctrl+Alt+Del` action,
removes the VM-specific jail, and records the Sandbox as `terminated`. The
public Session remains `idle` and can allocate a replacement Sandbox on its
next activation. If graceful shutdown fails or times out, process termination
falls back to SIGTERM/SIGKILL. A cleanup failure returns
`runtime_unavailable` with HTTP 503 so another DELETE can retry it.

## Host configuration

`config.example.json` documents the host-owned Firecracker and jailer binaries
and jailer UID/GID, runtime directory, SQLite state database, boot profiles,
trusted `image_catalog` path, and `config_encryption_key_file`, plus the
`network` object (`ip`, `iptables`, and `ip6tables` binaries, the private IPv4
address pool, and guest egress DNS resolvers and allowed ports), the
`host_pool` prepared-Sandbox capacity per snapshot profile (zero disables it),
and the `limits` and `timeouts` objects described below.
`image-catalog.example.json` shows the separately versioned workload catalog.
The optional `workspace` object
selects the trusted host JuiceFS mount root and admin root and the host
`exportfs`, `juicefs`, and `mountpoint` binaries. Redis and MinIO credentials
remain owned by the host-side JuiceFS gateway and are never delivered to fire-fuse or a guest.
Configuration decoding is strict, and all paths must be absolute. Profile names
are the only boot-artifact selector exposed to API callers.

When workspace configuration is present, fire-fuse creates the workspace
directory, installs an exact client-IP NFS export and firewall allowance, and
sends the workspace ID, host gateway IP, and export path to the restored guest
in its vsock assignment. The guest assignment agent writes those coordinates
into root-only files under `/run`, then mounts that export at `/workspace`.
A workspace-enabled VM reaches
`running` only after the agent confirms `/workspace` is a mount point and
reports the mount to the host. A failed mount therefore fails the assignment and
rolls back the Firecracker, network, and jail resources.

The SQLite store holds a non-blocking exclusive lock in a sibling `.lock` file
for its entire lifetime. A second fire-fuse process using the same state path
fails before opening the database or managing host resources.

The required `limits` object declares host-wide ceilings for active VMs, aggregate
guest memory, and concurrent starts. Admission reserves 512 MiB per VM; the
runtime reserves additional capacity before each hotplug request. Concurrent starts wait on
a context-aware gate. The VM ceiling cannot exceed the number of /30 guest
networks in the configured address pool. Guest vCPUs are not reserved host CPU;
each Sandbox instead has equal cgroup CPU weight and a four-core ceiling.
See [Sandbox CPU and memory](docs/sandbox-resources.md).

The required `timeouts` object uses duration strings such as `"5s"` and
declares bounds for the full startup operation, API socket readiness, graceful
guest shutdown, graceful and forced process shutdown, cleanup/rollback work,
HTTP request reads, keep-alive idleness, and HTTP server shutdown. All
durations must be positive.
`http_request_read` bounds reading a request, including its body, while
`http_idle` bounds an inactive keep-alive connection. No HTTP write deadline is
set because a synchronous create response can legitimately take the configured
startup duration. HTTP draining uses `server_shutdown` independently from VM
termination, whose outer budget is the sum of the guest, graceful process,
forced process, and cleanup windows. If graceful HTTP draining reaches its
deadline, fire-fuse force-closes the remaining server connections before
runtime cleanup.

The jailer identity must use a non-root UID and GID. Per-VM jail paths follow
`RUNTIME_DIR/FIRECRACKER_NAME/VM_ID/root`, with only that VM-specific directory
eligible for eventual cleanup.

Jail preparation bind-mounts the kernel and both configured root filesystems
into the jail read-only. An embedded early-boot init creates a capped, per-VM
tmpfs OverlayFS upper layer for the platform root before starting systemd; the
guest creates a separate capped upper layer for the container root. Guest
writes therefore cannot alter any configured boot artifact. Partial
preparation and bind mounts are rolled back, while an existing VM directory is
never overwritten.

The process launcher now constructs the jailer command, supervises the resulting
process without daemonizing it, and waits for a real Unix API socket. Early exit,
startup timeout, cancellation, and non-socket paths are reported distinctly.

The concrete supervisor composes profile and image resolution, snapshot input
preparation, process launch, snapshot restore, guest assignment, an in-memory
process registry, and reverse-order rollback.
The executable loads this supervisor from `-config`; configuration validation
first checks structure, then verifies `/dev/kvm` access; enabled IPv4 forwarding;
executable Firecracker, jailer, `ip`, and `iptables` binaries; readable non-empty
boot images; a writable runtime directory; and permission to assign jail
ownership to the configured UID/GID before opening the HTTP listener.

The Firecracker package contains a tested HTTP client pinned to a Unix domain
socket. Tenant startup uses its typed snapshot-load, drive-patch, resume, and
shutdown commands. Machine configuration and `InstanceStart` remain isolated
to the offline snapshot builder. The restored interface is attached to the
request's existing host TAP device.

Pass `-listen` to use a different loopback address or port. Sending `SIGINT` or
`SIGTERM` stops accepting HTTP work, then concurrently terminates every tracked
microVM. HTTP draining and runtime cleanup use independently configured
deadlines.

The networking package now defines a tested TAP lifecycle boundary. It derives
15-byte-safe interface names from VM IDs, assigns the configured jail identity,
marks each link with a `fire-fuse:<vm-id>` alias, assigns the host-side /30
address, brings links up, rolls back partial creation, and treats missing-device
deletion as success. It also reserves collision-free
/30 subnets from a private IPv4 pool, giving each active VM stable host, guest,
and locally administered MAC addresses.

The supervisor now reserves an address, creates the TAP, and attaches it to
Firecracker before boot. It also installs outbound, established-return, and
masquerade rules for that guest. Termination and failed starts remove firewall
rules before deleting the TAP, releasing its address, and deleting its jail. It
tags every rule with a deterministic `fire-fuse:<tap-name>` iptables comment so
ownership can be inventoried without matching unrelated firewall policy. It
appends a static Linux `ip=` argument so a kernel built with IP autoconfiguration
support assigns the guest address and uses the host TAP address as its gateway.
Startup verifies that `/proc/sys/net/ipv4/ip_forward` is `1`; fire-fuse does not
mutate this host-wide kernel setting.

## Restart recovery policy

Startup cleans up interrupted runtimes before accepting new lifecycle requests;
it does not reattach to microVMs that survived a fire-fuse process crash. A live
recorded process is verified by PID, Linux boot ID, and process start time, then
terminated and observed through a Linux pidfd so PID reuse cannot redirect a
signal. Stale process identities are never signaled. Cleanup checkpoints each
released firewall, TAP, address, and jail resource in SQLite; any incomplete
recovery prevents the HTTP listener from opening. Jail and socket paths remain
derived from trusted host configuration rather than persisted as authoritative
paths. Startup also inventories the reserved jail directory and deterministic
TAP namespace, plus tagged rules in the `FORWARD` and `POSTROUTING` chains.
Unowned resources are reported and block startup, but are not automatically
deleted.
Candidate TAPs must have an alias whose VM ID derives that exact deterministic
interface name. Missing or mismatched aliases are reported as ambiguous, while
valid aliases without a durable record are reported as owned orphans.
Each VM directory contains a strict, versioned `fire-fuse-runtime.json`
manifest binding it to its derived VM ID. Missing, malformed, mismatched, and
symlinked manifests are treated as ambiguous rather than trusted as ownership.
An interrupted `starting` record with no recorded host-resource ownership is
marked `failed` with the stable `runtime_start_failed` reason, so it cannot
remain permanently queued or consume admission capacity after restart.

The platform guest supplies its resolver file inside the container image at
runtime. Host resolver policy remains an operator responsibility.
