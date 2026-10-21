# Release activation consistency

## Problem and invariant

`deploy_api` currently replaces the public App script, and
`materialize_build` changes Fire Fuse's `active_build_id`, before the two
release components have both succeeded. `failComponent` changes only Console
records. `rollbackForUser` changes only Console records as well. A failed or
rolled back release can therefore keep serving API requests or supply the
Build for new Sessions. Fresh host replay currently ends with the newest
materialized Build even when an older release is active.

The required invariant is: a failed, cancelled, or superseded release never
supplies the API code or Build for a newly admitted request or Session after
the corresponding transition is reported complete. If the providers cannot
prove a consistent active release, admission fails closed until reconciliation
completes. Existing Sessions keep their original Build identity.

The chosen availability policy permits a brief API and new-Session admission
pause during deployment and rollback. A provider outage or interrupted
activation may extend that pause until both sides can be reconciled; neither
side may reopen independently to shorten it.

## Activation protocol

1. Stage each API artifact under an immutable, release-scoped dispatch script
   name and each prepared Build in Fire Fuse without changing the active
   script or `active_build_id`. Record provider identities with their release
   component outputs. A retry must verify the same artifact digest and never
   overwrite a different release's script.
2. Add a durable per-App activation operation, serialized with deployment,
   cancellation, rollback, configuration projection, and archive operations.
   Its target is a release whose two components succeeded, or a previously
   active release selected for rollback. The operation records the previous
   and target script and Build identities before contacting either provider.
3. Close public API and new Session admission for the App. Both admission
   paths must acknowledge closure before either live pointer changes. A
   missing gate, unknown generation, or unavailable gate rejects new work.
4. Select the target Build on Fire Fuse and target script in the dispatcher.
   Read both identities back. The dispatcher must use a strongly consistent
   per-App routing decision; the existing eventually consistent `APP_ROUTES`
   KV remains only a slug-to-App lookup. Configuration projection must apply
   to the staged script before it can be selected, including current secrets.
5. In one Console transaction, make the target release and App active and
   supersede the previous release. Then reopen both admission gates for that
   exact generation. The API returns success only after readback confirms the
   selected identities and open gates. A partial failure leaves admission
   closed and the durable operation retryable; it never reports rollback or
   activation complete.
6. Failure or cancellation of a staged release retires only its staged
   artifacts. It never changes live pointers. Reconciliation on worker
   startup compares Console's active release to both provider identities and
   keeps admission closed until the durable operation settles.
7. Fresh host replay restores every retained Build, then explicitly selects
   the Build for `apps.active_release_id`. It must not rely on replay order.

## Compatibility and migration

- Existing active Apps have a stable script named by App ID. Register that
  script as the current release's provider identity during migration; use
  release-scoped names only for subsequent candidates. Read back the current
  Fire Fuse Build before enabling the new protocol. Refuse activation on a
  mismatch and require reconciliation.
- The existing App route and API configuration operations currently target
  the stable App script. They must target the selected release script, and
  configuration changes must be serialized with activation so rollback does
  not restore stale bindings or secrets.
- Keep rollback-eligible script and Build artifacts until no active or
  selectable release references them. Archive must remove all release-scoped
  scripts only after admission is closed.
- App DELETE now queues archive work and returns HTTP 202. The private control
  worker serializes archive with activation, waits for earlier provider work,
  closes and reads back both gates, archives the compute registration and its
  existing Sandboxes, then marks the App archived. It removes the stable and
  every release-scoped script afterward.
  Cleanup retries behind closed gates after an uncertain provider response.
- Rolling out the dispatcher and Fire Fuse admission gates precedes enabling
  staged deployments. An older component must not silently ignore a new
  activation generation.

## Rollout order

1. Stop the Console control worker and prevent new release requests. Apply the
   Fire Fuse schema and binary update first. The migration starts all existing
   Apps with new-Session admission closed; verify the internal gate readback.
2. Deploy the dispatcher Durable Object and its control credential. Its
   missing state rejects public API requests. Use the same 64-character hex
   `RELEASE_GATE_TOKEN` in the Cloudflare environment and the control worker's
   Ansible Vault variable `vault_release_gate_token`. Verify no existing App
   owns the reserved `release-gate` slug before this step.
3. Apply Console migration 0018 and deploy the control worker. Its startup
   audit queues activation only for Apps with a missing or mismatched gate,
   script, or Build. Historical active API artifacts are restaged under their
   release IDs. Verify all such jobs succeed and both gates read back open
   with the same generation before allowing release requests again.
4. For fresh-host replay, keep new Sessions closed after restoring the Builds.
   Restart the control worker to reconcile the edge and host selections before
   opening admission.

If a provider is unavailable during cutover, keep both ingress paths closed
and retry the activation operation after restoring the provider. Do not reopen
one provider manually or select a Build without a closed host gate.

## Required validation

- Force both component completion orders, then make the other component fail
  or time out. Verify the old release continues serving and no new Session
  selects the candidate Build.
- Inject failure before and after every gate closure, provider selection,
  readback, Console commit, and gate reopening. Verify that only the selected
  active release serves, or new work receives a fail-closed response.
- Test A-to-B activation and B-to-A rollback, including API requests, new
  Sessions, provider outage, retry after worker restart, and duplicate
  activation commands.
- Replay an App whose newest materialized Build belongs to a failed release
  and one whose active release was rolled back. Assert the Build selected
  after replay matches `apps.active_release_id`.
- Exercise configuration and secret rotation across activation and rollback,
  plus App archive and artifact retention, to ensure old scripts cannot be
  reselected with stale bindings.

This is a coordinated cross-provider change. Updating only Console release
rows, adding best-effort compensation after a provider mutation, or switching
the dispatcher through eventually consistent KV leaves the reported
security boundary open.
