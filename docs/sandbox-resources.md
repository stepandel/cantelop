# Sandbox CPU and memory

Every production Sandbox is a Firecracker microVM with one guest shape: four
vCPUs and 512 MiB of boot memory, plus a virtio-mem range of a further 4096 MiB
in its snapshot. Developers do not choose a size. The jailer and guest isolation
boundary are the same for every Sandbox.

## CPU

The vCPUs describe maximum guest parallelism, not dedicated host cores. Every
Sandbox cgroup has equal `cpu.weight` (100) and a `cpu.max` ceiling of
`400000 100000`, or four cores of host CPU time per 100 ms period. Runnable VMs
share available CPU; idle VMs do not reserve a CPU share. The ceiling includes
Firecracker's own threads. Host admission does not sum guest vCPUs.

## Memory

Fire Fuse grows a Sandbox's memory when the guest reports low `MemAvailable`.
A Sandbox can use at most 4608 MiB of guest memory.

The memory controller starts immediately after guest assignment and polls the
private guest socket on port 19095 every second. Firecracker status requests
and guest probes run concurrently with separate two-second deadlines. Growth begins below
256 MiB available or one third of guest total memory. Each request adds
512–1024 MiB, in 256 MiB increments, aiming for at least 512 MiB or half the
current guest memory available. A smaller increment is allowed when only
256 MiB of host capacity remains or the Sandbox is approaching its ceiling.

Shrink requires five continuous minutes above 70% available memory. Each
request removes 256 MiB only if the remaining availability stays above both
growth thresholds. Three consecutive failed probes, a pending resize, or renewed
pressure resets the idle timer; a single missed probe does not. If pressure
returns or the guest probe fails three consecutive times during memory removal,
the controller cancels the pending shrink by requesting at least the
still-plugged allocation.

A growth request reserves host capacity before it is sent. A shrink releases
that reservation only after Firecracker reports that the guest has actually
unplugged the blocks. Device progress and completed shrinks are reconciled
even when the guest availability probe is unavailable. A guest that cannot
unplug memory keeps its reservation. After three consecutive failed guest
probes, the controller may request up to 512 MiB of emergency headroom, subject
to the same host budget and guest ceiling. Later outages receive no more until
a shrink reclaims memory, so intermittent failures cannot ratchet growth. It never
shrinks or reports inferred guest usage from a failed probe.

The `guest_memory_mib` field in sandbox usage is the last observed guest size;
`memory_current_bytes` is the host cgroup's current charge. Probe failures are
logged on the first failure and once per minute while they persist, with a
recovery record when the probe responds again. Resizes pending for at least
15 seconds emit a warning with the requested and plugged sizes, repeated at
most once per minute. Fire Fuse also logs completed resizes, denied growth,
and failed resize requests with the Sandbox ID. A lost growth reply stops
further resizing and retains the reservation until that VM exits, since the
request may already have been applied. A growth request Firecracker rejects
was not applied, so resizing continues.

To keep admission and concurrent growth independent, the memory controller
reserves 512 MiB for every configured VM slot, including currently empty
slots. The rest of `limits.max_total_memory_mib` is the aggregate hotplug
budget. Each VM cgroup has a 5120 MiB `memory.max` limit, covering its 4608
MiB guest ceiling and 512 MiB of VMM overhead. Set the aggregate limit below
physical host RAM to leave room for the supervisor and host services.

Firecracker's hotplug target is asynchronous, so a sudden allocation can still
reach guest OOM before additional memory is plugged. The 4096 MiB hotplug
ceiling is fixed in the snapshot and requires a new release to change. A guest
that cannot free movable pages may not complete a shrink request. The guest
kernel needs `CONFIG_MEMORY_HOTPLUG`, `CONFIG_MEMORY_HOTREMOVE`, and
`CONFIG_VIRTIO_MEM`; the snapshot builder appends
`memhp_default_state=online_movable` to its boot arguments.

## Capacity qualification

Host admission counts active VMs, their memory reservations, and simultaneous
starts. The fixed warm-slot pool is a separate limit on live Sandboxes when
enabled. A host configured for N active Sandboxes needs `max_vms` and warm-slot
capacity of at least N, an address pool with at least N `/30` subnets, and
physical memory above `limits.max_total_memory_mib` for the host and VMM.

Increase host limits only after a representative host passes a staged 25, 50,
then 100 active-Sandbox run. Include simultaneous CPU bursts, memory growth and
shrink, Workspace I/O, startup and cleanup churn, p95 request latency,
`cpu.stat` throttling, CPU and memory pressure, guest OOM counters, and daemon
restart recovery. A four-vCPU guest count does not guarantee a minimum CPU share
when 100 VMs are runnable; the observed share depends on available host cores
and competing work.

The cold cutovers that introduced this shape are recorded in
[history/elastic-cpu-cutover.md](history/elastic-cpu-cutover.md) and
[history/elastic-memory-cutover.md](history/elastic-memory-cutover.md).
