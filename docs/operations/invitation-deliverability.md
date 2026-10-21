# Invitation email delivery

An API success means Resend accepted a message. A delivered webhook means the recipient's mail server accepted it; neither proves inbox placement.

## Configuration and rollout

1. Apply migration `0013_invitation_delivery` before the new console. It is additive except for expanding the existing delivery constraint. Existing invitations and tokens remain valid. Old console code tolerates the added fields/statuses, but cannot enforce new suppression preferences, so do not roll back to a sender without suppression checks after launch.
2. Verify `notify.cantelop.com` in Resend. Its DKIM host is `resend._domainkey.notify`; the two sending CNAMEs are `rsend.notify -> rsend.forge.rmta.net` and `send.notify -> send.forge.rmta.net`. Keep receiving disabled and open/click tracking disabled. Do not change Google Workspace MX/SPF/DKIM.
3. Configure production-only secrets in Vercel:
   - `CANTELOP_INVITATION_RESEND_API_KEY`: sending-only, scoped to `notify.cantelop.com`. Falls back to `RESEND_API_KEY` when absent, for compatibility.
   - `CANTELOP_INVITATION_FROM`: `Cantelop <invitations@notify.cantelop.com>`.
   - `CANTELOP_INVITATION_REPLY_TO`: `stepan@cantelop.com` (monitored Workspace inbox).
   - `CANTELOP_INVITATION_ORIGIN`: `https://cantelop.com`.
   - `CANTELOP_INVITATION_OPTOUT_SECRET`: independent cryptographically random secret, at least 32 characters. Preserve it across deployments so old preference links keep working. Do not reuse OAuth or webhook secrets.
   - `RESEND_WEBHOOK_SECRET`: signing secret for `https://cantelop.com/api/webhooks/resend`.
   - `CRON_SECRET`: independent random secret for Vercel's daily retention job.
4. Subscribe that webhook to `email.sent`, `email.delivered`, `email.delivery_delayed`, `email.failed`, `email.bounced`, `email.complained`, and `email.suppressed`. The handler verifies the raw body and timestamp with Svix before database access. There are no open/click events.
5. Deploy, verify preference/privacy pages, and send an approved real invitation. Verify its Resend id is stored, the webhook returns 204, and delivery updates without implying inbox placement. Check Gmail Show original for SPF/DKIM/DMARC and actual Return-Path.
6. Retest placement with a fresh authorized tester list. Do not reuse the September 22 list without fresh authorization. Use a recipient-bound test invitation and never grant tester mailboxes app access. Preserve the tester list after the run.

## Behavior and privacy

- Per-owner budget: 20 invitations/hour. Per-recipient budget across owners: 3/day and at least 60 seconds between sends. Resending replaces the old link. Database locks serialize each budget.
- Opt-out tokens contain an address fingerprint and HMAC, not an email address or acceptance token. They only authorize stopping invitation mail. A browser GET has no side effect; the page requires confirmation. RFC 8058 one-click POST is also supported.
- Suppression is checked during preparation and again immediately before the network send. Mail already in transit when someone opts out can still arrive.
- Hard bounces, complaints, and provider suppressions stop further invitation mail. Transient bounces are delayed, not permanently suppressed. Preferences do not remove existing membership or prevent adding an already registered account without sending email.
- Signed webhook tags correlate callbacks arriving before the API response. Provider ids and recipient matching prevent unrelated messages binding. Event ids deduplicate replays; lower-priority events cannot overwrite delivered or terminal failure states.
- Daily cron deletes up to 1,000 invitations whose expiry is more than 90 days old; associated minimal event records cascade. Suppression fingerprints remain to honor preferences. If the cap is reached, investigate backlog and increase cleanup frequency. Fingerprints are pseudonymous personal data, not anonymous data.
- The notice at `/invitations/privacy` covers this feature only. It is not a full company privacy policy or a legal determination. Confirm legal basis, controller details, international transfer arrangements with providers, and retention requirements with the operator before claiming GDPR compliance. Marketing requires a separate consent flow.
- Re-enabling a recipient is an operator action only after the recipient asks. Verify the reason: resolve hard bounce causes first; do not automatically lift complaint suppression. Delete only the exact suppression fingerprint after verification and record the request in the operator's support process.

## Diagnosis and escalation

The September 22 baseline landed in spam for 14/15 testers (Gmail 5/5, Outlook 5/5, Yahoo 4/5). All 15 reached a mail server. The audit passed DKIM, DMARC, all 38 blocklists, link reputation, and SpamAssassin (-0.1). Its SPF softfail evaluated the visible From domain, while the actual earlier Gmail headers authenticated the `rsend.cantelop.com` return path successfully. Verify original headers before changing SPF. A root SPF change is not justified by this audit alone.

The text-to-HTML warning is a heuristic, not evidence that more markup improves delivery. Keep HTML simple and maintain the text alternative. Missing unsubscribe and CAN-SPAM warnings describe the same missing capability. Amazon SES detection is informational.

Keep DMARC at monitoring (`p=none`) until aggregate reports cover legitimate Workspace and Resend traffic. Enforce gradually only after reviewing those reports. BIMI depends on additional policy/certificate work and is not an immediate inbox-placement fix. MTA-STS is a separate inbound transport-security project. A new sending subdomain separates traffic but starts without established reputation.

If placement remains poor after authenticated sending and template changes, provide Resend support the baseline report and message ids and request analysis of the shared sending pool/account. Do not buy a dedicated IP or artificial warm-up service without volume evidence and explicit budget approval. Build reputation with legitimate, expected invitations at normal volume.
