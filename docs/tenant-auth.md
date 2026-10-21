# Customer identity boundary

Cantelop currently treats one authenticated user as one tenant. A normalized,
provider-verified email identifies the user, and stable GitHub and Google
account IDs are linked sign-in methods for that user. Signing in through either
provider therefore reaches the same Apps, quota, and permissions. After a
provider identity is linked, its stable provider account ID keeps it attached
even if the provider later reports a different email; the account's address
follows the newly verified one unless another account already holds it.
Accounts that predate verified linking were merged only on Google-verified
addresses, and GitHub-created accounts hold a placeholder address until their
next sign-in reports a verified one. If that address already belongs to another
account, the legacy account is reconciled into it as described below. Google sign-in requires
an address Google reports as verified. GitHub sign-in independently reads the
account's email list and requires a verified address; a public profile email by
itself is not trusted for linking. These checks also protect the email-based
`ADMIN_EMAIL` operator grant.

The product trust path is:

1. GitHub or Google authenticates the browser to the NextAuth console.
2. The console resolves the provider subject into an opaque `usr_*` ID in
   PostgreSQL and records it in the signed NextAuth session.
3. Same-origin browser routes derive the user only from that session and call
   the product service directly.
4. CLI authorization returns a 30-day Cantelop refresh credential. The CLI
   exchanges it at the console for a 60-second Ed25519 access token whose
   subject is the same user ID and whose scopes are `tenant:read` and
   `tenant:write`.
5. Public `/v1/*` routes verify that access token at the console origin and run
   App-access-scoped PostgreSQL operations. An App's owner and its active admin
   collaborators pass this boundary; owner-only mutations perform a stricter
   ownership check.

The CLI stores no GitHub or Google credential. Its Cantelop refresh credential is stored
in the operating system's user configuration directory with mode `0600`.
`CANTELOP_CONTROL_PRIVATE_KEY` is console-only signing material and must not be
copied to Fire Fuse, Cloudflare Workers, customer artifacts, or object storage.

## Console domains

The console serves `https://cantelop.com`, `https://www.cantelop.com`, and
`https://console.cantelop.dev` without redirecting between them. NextAuth v4
uses Vercel's forwarded host to build OAuth callbacks when `NEXTAUTH_URL` is
unset. The auth route accepts only these three HTTPS origins. Customer browser
writes require an Origin header matching the request's allowed origin.
Cookies remain host-only: users sign in separately on each domain, but the
same provider account resolves to the same Cantelop user and resources.

### Deployment configuration

1. Copy the existing production `NEXTAUTH_URL` value to
   `CANTELOP_CONTROL_ORIGIN` (currently `https://console.cantelop.dev`). This
   stable CLI/API token issuer must not change, or existing credentials stop
   verifying. The legacy variable remains a fallback for existing workers.
2. Remove `NEXTAUTH_URL` from the Vercel console environment. Keep
   `NEXTAUTH_SECRET` unchanged and Vercel system environment variables exposed
   (`VERCEL=1`). Do not set `NEXTAUTH_URL` per request: shared processes may
   serve all domains concurrently.
3. Add the new callback URLs to the existing provider clients, retaining the
   old URLs:

   | Provider | Existing callback | Additional callback |
   | --- | --- | --- |
   | GitHub | `https://console.cantelop.dev/api/auth/callback/github` | `https://cantelop.com/api/auth/callback/github` |
   | Google | `https://console.cantelop.dev/api/auth/callback/google` | `https://cantelop.com/api/auth/callback/google` |

   GitHub supports multiple callback URLs in its [OAuth app settings](https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/creating-an-oauth-app).
   Also register `https://www.cantelop.com/api/auth/callback/github` and
   `https://www.cantelop.com/api/auth/callback/google` for the existing www alias.
   For Google, add the additional URI to the client's authorized redirect URIs
   and configure `cantelop.com` as an authorized domain if required by the
   consent-screen settings. Keep the same client IDs and secrets.
4. Redeploy. On each domain verify GitHub and Google sign-in, sign-out, customer
   writes (Apps, Workspaces, configuration), and CLI authorization. The browser
   must stay on the domain where it started. Existing `.dev` sessions should
   remain valid because their cookie name and signing secret are unchanged.

If a fixed `NEXTAUTH_URL` remains and conflicts with the requested origin, the
OAuth route returns `503 auth_origin_configuration` rather than initiating a
flow on the wrong domain. Unrecognized hosts receive `400 invalid_host`.
For local development, set `NEXTAUTH_URL=http://localhost:3000`; loopback HTTP
origins are accepted only outside production.

## Product authorization

PostgreSQL is authoritative for users, ownership, quotas, Apps, Workspaces,
configuration, releases, and operation status. App creation records the App
and Session runtime in one quota-checked transaction. SDK Workspace opening
materializes each durable Workspace separately. Reads and mutations lock or
filter by the signed user subject;
resources owned by another user are returned through the same non-enumerating
not-found path as missing resources.

Every new user receives persisted limits of 5 Apps, 16 vCPUs, and 16 GiB of
Session memory. Each standalone App and each [Fleet](managed-fleets.md) consumes
one App slot; a Fleet's installations consume no additional account slots.
App and Fleet creation enforce that shared limit inside their creation
transactions; a rejected request returns
`429 quota_exceeded` and creates no partial App. Runtime Sandbox
admission independently enforces the host VM, vCPU, memory, and concurrent-start
ceilings. The persisted user vCPU and memory limits are not currently charged
during App creation because Apps no longer reserve a fixed Session capacity.

Customer deployment metadata and small API modules are validated by the
console. Large Session runtime bytes and API bytes use short-lived, App-bound direct
upload targets in private object storage. Background control workers alone
hold Cloudflare, object-store, and private compute credentials.

## VM-host boundary

Fire Fuse publishes no customer control API. The former
`fire-fuse-tenant-api` binary, port `8083`, public nginx site, and console
verification key have been retired.

The VM host accepts only authenticated private provider commands on numeric
loopback listeners. Its configuration cache is populated by full revisioned
projections from the external control worker; Fire Fuse cannot mutate product
configuration or call Cloudflare. Build materialization receives an expiring
artifact URL and verifies the expected digest and size without receiving
object-store credentials.

Fire Fuse has no identity resolution, quota administration, tenant CRUD/read,
or direct customer Build upload handler. The opaque user ID carried by private
provider commands remains in `compute_tenants` solely as a provider-local
ownership key. `app_tenants` relates that key to projected Apps. Neither table
is an authentication identity or product record, and the VM-host schema has no
email, account-status, or product-quota columns.

No `/v1/internal/*`, host lifecycle route, Build upload listener, observer
route, or Fire Fuse control token may be exposed through public DNS, nginx, or
the Cloudflare Tunnel. Public customer traffic terminates at the Cloudflare
dispatcher or the console API, depending on whether it is runtime traffic or
control traffic.

## Reconciling migrated GitHub accounts

A fresh verified sign-in for an exact legacy placeholder such as
`github-20438019@unverified.invalid` now reconciles the account when another user
already owns the verified email. The account that already owns the email remains
the canonical user. The transaction moves linked identities, app ownership,
collaborator access and attribution, quotas, operation/release/upload history,
and invitation references (when installed), then removes the placeholder user.
Quotas use the larger limit from either account, not their sum. Colliding request
keys on historical source records receive new internal keys; the canonical
account's keys remain unchanged.

This applies only to legacy placeholders authenticated again through their
matching stable provider identity. A normal account whose provider email changes
to another account's address remains linked to its existing user. Neither a
suspended source nor a suspended destination can reconcile through sign-in: a
suspended source is refused as always, and a suspended destination leaves the
active legacy account signed in and separate under its placeholder address.

`apps.compute_tenant_id` preserves the original private compute ownership key for
moved apps. This field is an opaque provider identifier, not a reference to a live
product user and never an authorization identity. The control worker resolves it
for registration, workspace creation, configuration, archive, purge, build materialization,
and cancellation. App IDs, workspaces, builds, runtime data, and external compute
tenants are not recreated or reassigned.

### Rollout and verification

1. Apply `0012_preserve_compute_tenants` and `0014_preserve_user_aliases` before
   deploying the updated console. These add the preserved compute key and the
   recorded user-ID mappings.
2. Deploy the updated **control worker before the console** so every private
   compute call understands preserved tenant keys before any account can merge.
3. Deploy the console. The affected user must sign out and complete a fresh
   GitHub sign-in. Refreshing an existing browser session does not reconcile it.
4. Confirm that GitHub and Google now resolve to the same user ID, the real email
   appears on that user, and both accounts' apps and memberships are available.
5. Subsequent merges atomically record the removed user ID as an alias
   of the canonical account. Existing browser sessions, CLI refresh credentials,
   and unexpired access tokens resolve that alias and check the canonical user's
   active status before accessing resources. Membership removal and suspension
   remain effective; aliases do not grant permissions independently.
6. Accounts removed before alias recording was deployed require a new sign-in /
   CLI login. Their mapping cannot be safely reconstructed from token email.
   An unresolved browser subject is signed out rather than appearing signed in
   while all App lookups fail. Provider sign-in resolves the existing identity to
   its canonical user. Credentials for canonical accounts retain their ID.

If the source account has a running control operation, that sign-in completes on
the legacy account and the merge is deferred to a later sign-in, once the
build/deployment has finished. This prevents a worker already holding the source
user ID from losing access mid-operation without locking the account out of the
console meanwhile. Queued operations transfer normally. Merges are serialized
against each other, and any error rolls back the entire merge.

The migration alone does not merge users. No existing GitHub profile email is
used as proof: only the verified address obtained during the new OAuth login can
trigger reconciliation. Do not manually copy a historical profile email into the
unique `users.email` column to bypass the verification step.
