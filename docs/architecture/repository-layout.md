# Repository architecture

Cantelop is organized around its three major product subsystems rather than
around the implementation that preceded the product:

```text
console/       Product UI, APIs, domain logic, and asynchronous workers
edge/          Cloudflare Workers and their shared transport contracts
fire-fuse/     Host compute subsystem
builder/       Builder host programs that are not part of Console
storage/       Pinned storage host builds of JuiceFS and MinIO
```

Supporting clients, cross-subsystem contracts, operator tools, deployment
code, and system tests live alongside those subsystems at the repository root.

## Hosts

Cantelop runs on three hosts. Each has one build command, one Ansible
playbook, and one deploy command:

| Host | Runs | Source | Build | Deploy |
|---|---|---|---|---|
| Builder | Control worker, image materializer | `console/workers/control`, `builder/` | `make builder-artifacts` | `make deploy-builder` |
| Storage | Redis, MinIO, JuiceFS volume | `storage/` | `make storage-artifacts` | `make deploy-storage` |
| Compute | Fire Fuse runtime, gateway, dispatcher, observer | `fire-fuse/`, `tools/` | `make compute-artifacts` | `make deploy-compute` |

Deployment code follows the same split. `platform/ansible/roles` and
`platform/ansible/playbooks` have `builder/`, `storage/`, `compute/`, and
`shared/` directories, where `shared/` holds the tunnels and dashboard that
span hosts. `platform/systemd` has `builder/` and `compute/` units; storage
services use their distribution or role-rendered units. Deploy storage first, then compute, then the
builder. See [Ansible host provisioning](../../platform/ansible/README.md).

## Console

The Console is one product surface with three source areas:

```text
console/
  core/        Product rules, application workflows, and adapters
  web/         Next.js application and request-bound entrypoints
  workers/     Independently executed background worker entrypoints
```

`core` must not depend on Next.js route handlers or worker process lifecycle.
Both `web` and `workers` may compose functionality from `core`.

## Edge

The Edge subsystem uses `edge/workers` for code deployed as Cloudflare
Workers. Cloudflare provisioning remains deployment infrastructure rather than
application code.

```text
edge/
  contracts/   Shared edge transport types and validation
  workers/     Dispatcher, outbound, origin broker, and user API Workers
```

## Fire Fuse

Fire Fuse is the complete host compute subsystem. Its independently executed
surfaces are organized by responsibility:

```text
fire-fuse/
  gateway/         Authenticated edge control and Message acceptance
  dispatcher/      Durable actor Message delivery
  runtime/         Session policy, lifecycle, builds, and persistence
  host/            Privileged host-resource implementations
  guest-runtime/   Programs and protocols that execute inside a microVM
```

Host-local request coordination is factored into named contracts and
capabilities shared by the gateway and dispatcher composition roots:

```text
fire-fuse/
  edgeprotocol/       Trusted edge metadata contract
  messageprotocol/    SDK Session runtime Message, output, and quiescence contract
  controlclient/      Authenticated loopback control-plane adapter
  sessionbroker/      Session identity and lifecycle semantics
  sessionevents/      Bounded process-local Session event replay
  sessionoutput/      Continuous Sandbox-to-Session output handoff
  messagedelivery/    Lease-protected Message delivery through quiescence
  sandboxhttp/        Private Sandbox HTTP transport policy
```

These are in-process packages, not additional services. Source placement follows
ownership rather than the first process that happened to use the code.

The runtime surface decides what should happen. The host surface implements
how the local machine makes it happen. Sandbox remains an internal isolation
abstraction: the Firecracker implementation belongs to the host surface and is
not the name of the broader runtime surface.

Within Runtime, shared value contracts, persistence infrastructure, and the
current microVM orchestration remain separate:

```text
fire-fuse/runtime/
  model/         Provider-neutral Runtime value objects and validation
  persistence/   Storage infrastructure, SQL repositories, schema, and files
  microvm/       Sandbox orchestration and adapters for the current backend
```

Persistence infrastructure must not acquire Sandbox lifecycle policy. Domain
callbacks in `microvm` decode and validate opaque Sandbox and host-slot records;
repositories under `persistence/sqlite` own their atomic storage transactions.
Database bootstrapping, file security, schema management, and connection
lifecycle also belong under `persistence`. The architecture checker enforces
that `persistence` cannot import `microvm` and that production `microvm` code
cannot access `database/sql` directly.

The main `fire-fuse` executable is a composition root. It may connect runtime
policy to host implementations, but business rules should not accumulate in
the command package.

## Builder

The builder host runs the Console control worker and the local image
materializer. The control worker is a composition of Console Core and remains
at `console/workers/control`. Builder-only programs live under `builder/`:

```text
builder/
  materializer/               Docker archive to Fire Fuse image conversion
  cmd/build-materializer/     Root-owned materializer socket service
```

The builder produces images that compute hosts boot, so it may import only the
shared Fire Fuse image contracts: `fire-fuse/config`,
`fire-fuse/guest-runtime/guestconfig`, `fire-fuse/host/hostpath`, and
`fire-fuse/runtime/sandboxprofile`. Fire Fuse must never import `builder/`.

## Dependency direction

- Console communicates with Fire Fuse through the private compute contract;
  it does not import Fire Fuse implementation packages.
- Edge communicates with the gateway through edge transport contracts.
- The gateway uses runtime contracts and clients, not persistence or
  Firecracker implementations.
- Actor Message delivery uses the Message delivery service, which holds a Session work
  lease until the SDK Session runtime reports runtime quiescence. Gateway HTTP
  coordination and queue dispatch run as separate processes; dispatcher and
  shared coordination capabilities must not import gateway implementation code.
- Session output collection is driven continuously per active Sandbox. Message
  lifecycle reconciliation observes its acknowledged watermark before declaring
  quiescence, but its polling interval must never pace the Session event stream.
- Runtime provider adapters depend on model contracts, not the current
  `microvm` orchestration or persistence implementation.
- Fire Fuse runtime code may depend on host interfaces. Host implementations
  must not depend on HTTP handlers or Console product logic.
- Guest runtime code must remain independently buildable from privileged host
  code.
- Shared code must name the contract or capability it represents. Generic
  top-level `common`, `shared`, and `utils` packages are intentionally avoided.

Operational names such as the `fire-fuse` binary, systemd units, and paths
under `/etc/fire-fuse` are stable deployment interfaces. Source organization
does not require those interfaces to be renamed.

## Enforcement

Run the complete repository check from the root:

```sh
make check
```

`scripts/check-architecture.py` rejects forbidden cross-surface imports before
they can erode these boundaries. Host code may consume Runtime model contracts,
but it cannot import Runtime implementations. Runtime provider adapters are
likewise prevented from importing `runtime/microvm`.
