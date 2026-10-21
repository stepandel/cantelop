# Builder

The builder host runs two services behind the builder-to-compute WireGuard
tunnel:

| Service | Source | Unit |
|---|---|---|
| `cantelop-control-worker` | [`console/workers/control`](../console/workers/control) | `cantelop-control-worker.service` |
| `cantelop-build-materializer` | [`builder/cmd/build-materializer`](cmd/build-materializer) | `cantelop-build-materializer.service` |

The control worker drives durable provider operations from the Console
database. It asks the materializer, over the root-owned
`/run/cantelop-builder/materializer.sock`, to turn a verified Docker archive
into a Fire Fuse image before registering it with a compute host.

Builder code may use only the shared image contracts from `fire-fuse/`; see
[repository layout](../docs/architecture/repository-layout.md#builder).
