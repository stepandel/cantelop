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

Fire Fuse grows a Sandbox's memory in 256 MiB steps when the guest reports low
`MemAvailable`, and attempts to remove idle memory after one minute. A Sandbox
can use at most 4608 MiB of guest memory.

The memory controller polls the private guest socket on port 19095 every two
seconds. Growth begins below 128 MiB available or 25% of guest total memory.
Shrink begins above 50% available. A growth request reserves host capacity
before it is sent. A shrink releases that reservation only after Firecracker
reports that the guest has actually unplugged the blocks. A guest that cannot
unplug memory keeps its reservation. The `guest_memory_mib` field in sandbox
usage is the last observed guest size; `memory_current_bytes` is the host
cgroup's current charge. Fire Fuse logs denied growth and failed resize
requests with the Sandbox ID.

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
