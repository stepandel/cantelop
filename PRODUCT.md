# Product

<!-- impeccable:product-schema 1 -->

## Platform

web

## Users

External developers building agents with the Cantelop SDK. They sign up,
create Apps, and deploy complete API + Session runtime releases — via the `cantelop`
CLI for deployment and the web console for observing and managing Workspaces
and Sessions. A single operator persona also exists: the platform operator
uses `/admin` for a global host view and the runbooks in `docs/` for
deployment, monitoring, and recovery. The infrastructure is Cantelop's, not
the customer's; it is a hosted service, not self-hostable.

## Product Purpose

Cantelop is a hosted full-stack infrastructure platform for agents, in the
mold of Vercel: developers build with the Cantelop SDK (`@cantelop/sdk`),
deploy with the `cantelop` CLI, and the platform runs their agent workloads
as isolated Sessions on Firecracker microVMs. Customers get durable,
addressable Workspaces selected through the SDK behind each App's public
hostname, cold Session activation,
and a management and observability console. Success means external developers can log in,
deploy, and run real agent workloads on the hosted platform in production.

## Positioning

Full-stack infrastructure for agents: what Vercel is to frontend apps,
Cantelop is to agents. The SDK + CLI + platform form one integrated path from
agent code to a running, addressable, microVM-isolated deployment — developers
never touch servers, schedulers, or sandbox plumbing. Not self-hostable and
not own-your-infra; the hosted, SDK-first agent focus is the position.
(User-confirmed direction; the precise differentiating claim versus agent
sandbox providers like E2B/Modal remains open.)

## Operating Context

- Deploys happen through the `cantelop` CLI (login via loopback callback
  against the production console); large artifacts go directly to object
  storage, never through the console.
- The Next.js console is the external product control plane: `/dashboard` is
  the customer view scoped to the signed session; `/admin` is the operator's
  global host view backed by the authenticated `fire-fuse-observer`.
- PostgreSQL is authoritative for users, quotas, Apps, Session runtimes, Workspaces,
  and durable operations; fire-fuse is a private loopback-only compute agent.
- Operations run against a Hetzner dedicated-server staging deployment with
  Ansible playbooks, systemd units, and documented recovery procedures.

## Capabilities and Constraints

- Resource hierarchy: User → App → Builds + Workspaces → Sessions → Sandbox
  (Sandbox is internal implementation vocabulary, not public). This vocabulary
  is established in `docs/domain-model.md` and used across console, CLI, and
  API, but the user has not marked it immutable — treat it as incumbent
  product language, not a locked commitment.
- One Linux host with KVM per deployment; no cluster scheduler. An internal
  platform-side implementation detail, never a customer-facing fact; the user
  did not mark single-host as a durable binding constraint, so scaling beyond
  one host is an open engineering question.
- Sessions are stable convenience identities whose `active` or `idle` state is
  derived from their current Sandbox allocation. Releasing a Sandbox never
  terminates its Session; a later activation can allocate a replacement behind
  the same Session ID. Workspaces have `archived_at` as their only lifecycle
  marker.
- API/error/lifecycle/durability contracts are documented in
  `docs/api-lifecycle.md` and `docs/openapi.yaml`; the console must not expose
  fire-fuse's `/v1` API publicly.
- Stack (existing): Go for fire-fuse, observer, gateway, and CLI;
  Next.js + React + Drizzle/PostgreSQL (Bun) for the console. Tool and framework
  releases are authoritative in their module and package manifests.

## Brand Commitments

- **Naming boundary (binding, user-confirmed):** "Cantelop" is the product
  name on every user-facing surface. "fire-fuse" is internal orchestrator
  naming (binaries, services, paths, `FIRE_FUSE_*` env vars) and must never
  leak into customer-facing UI, copy, or URLs.
- Domain: `cantelop.dev` (public App hostnames are `<app-slug>.cantelop.dev`;
  Workspaces are selected through the SDK rather than public hostnames).
- An ASCII-art CANTELOP wordmark exists in the README; no further logo or
  visual identity assets are on hand.
- The console carries an incumbent visual world documented in
  `console/web/DESIGN.md` (dense instrument panel: paper-white ground, ink
  text, IBM Plex Mono data, hairline rules). That file is the visual
  authority for the console surface.

## Evidence on Hand

- Real, extensive operator documentation in `docs/` (operations, systemd,
  storage performance benchmarks, security audit, release handoff).
- A working staging deployment path on Hetzner (`platform/hetzner/`).
- No testimonials, case studies, customers, pricing, or public benchmarks
  exist. **Pre-first-production (user-confirmed):** there is no production
  deployment and no real tenants yet. Future surfaces must not fabricate
  customer proof, logos, usage numbers, or pricing.

## Product Principles

1. The SDK is the front door: every surface should reinforce one integrated
   path — build with the SDK, deploy with the CLI, run on the platform —
   with no server or sandbox plumbing exposed to the developer.
2. Small and legible over clustered and clever: a narrow HTTP API and
   infrastructure the team can fully understand beat scheduler
   sophistication. (An internal engineering principle; customers never see
   the hosts.)
3. The boundary is sacred: product surfaces speak Cantelop and the public
   domain model; orchestrator internals stay private.
4. Truth over aspiration: pre-production status means no invented proof;
   evidence is the docs, the contracts, and the running system.
