# App admin invitations

The app owner can add an admin by email from **Access**. An existing active
account gets access immediately. An address without an account gets a pending
invitation and a transactional email through Resend. A pending invitation grants
no access until its recipient signs in with Google or GitHub and accepts it.
Both providers already require verified email and resolve matching addresses to
one product account.

## Deployment

1. Install the [Resend integration](https://vercel.com/marketplace/resend) on the
   console's Vercel project, or set `RESEND_API_KEY` as a server-only environment
   variable. Give the key sending permission for the intended domain.
2. Verify a sending domain in Resend and set `CANTELOP_INVITATION_FROM`, for
   example `Cantelop <invites@notifications.example.com>` using that domain.
3. Set `CANTELOP_INVITATION_ORIGIN` to the console origin where recipients should
   sign in, for example `https://cantelop.com`. It must be an HTTPS origin with no
   path, credentials, query, or fragment. HTTP loopback is allowed for local
   development. When omitted, it uses `CANTELOP_CONTROL_ORIGIN`; with neither
   set, every send fails rather than linking to a different deployment. This
   domain must support the existing OAuth callbacks.
4. Apply the control database migrations (`cd console && bun run db:migrate`)
   before serving the updated console. Migration `0011_app_invitations` only adds
   the invitation table and its constraints/indexes. The Vercel build's existing
   migration workflow also applies it.
5. Deploy with those environment variables. Send an invitation to an address you
   control that does not already have a Cantelop account, sign in using that
   address, and accept it.

The implementation calls the Resend HTTPS API directly; no SMTP server or new
runtime dependency is required. Never expose the key as a `NEXT_PUBLIC_*` value.
See [Resend's send API](https://resend.com/docs/api-reference/emails/send-email).

## Lifecycle and failure recovery

- Links expire after seven days. Owners can resend or revoke pending invitations,
  including expired invitations. Resending revokes the previous link and creates
  a fresh invitation with a fresh token and expiry.
- The owner can send at most 20 invitations per hour across their apps, including
  resends. An individual invitation cannot be resent within one minute.
- Only app owners can view or manage pending invitations. Suspended owners cannot
  send them, and suspended recipients cannot accept them. Archived apps cannot
  receive or redeem invitations.
- Acceptance requires both the current account email and the provider-verified
  session email to match the invitation. It consumes the invitation and grants
  membership in one transaction; concurrent acceptance succeeds only once.
- Email is sent after the invitation transaction commits, with a ten-second
  timeout and a Resend idempotency key. `sent` means Resend accepted the request,
  not confirmed inbox delivery. There is no delivery/bounce webhook in this version.
- Missing configuration or provider failure leaves a visible **Send failed**
  invitation. Fix configuration if needed, then use **Resend**. An interrupted
  request left in `sending` shows **Retry send** after a minute. Sends are awaited
  by the request; no in-process background task is relied on after a Vercel response.
- Only a SHA-256 hash of the random 256-bit token is stored. The email link uses
  a URL fragment so the token is not included in HTTP access logs or referrers.
  The acceptance page moves it into tab-local `sessionStorage` before OAuth and
  clears it after acceptance. Browser storage must be available; complete sign-in
  in the same tab, or reopen the original email link afterward.
- Adding an existing account also revokes any pending invitation for its address,
  so a stale invite cannot restore access after the admin is subsequently removed.

## Validation

Run `cd console && bun test` and `bun run typecheck`. The database lifecycle test
needs a disposable PostgreSQL database:

```sh
CANTELOP_TEST_DATABASE_URL=postgresql://postgres@127.0.0.1:55439/postgres \
  bun test core/db/invitation-repository.integration.test.mjs
```

It applies the complete migration chain and checks first-time invitations,
provider account linking, immediate existing-account grants, delivery status,
resend/revocation, expiration, wrong identities, suspended accounts, archived
apps, concurrent acceptance, and concurrent send limits. Preview the access UI
locally at `/preview/app?section=access` for invited, failed, and expired states.
