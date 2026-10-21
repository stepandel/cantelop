# Google policy review — 2026-09-09

## Finding and limits

The supplied Google Transparency Report screenshot classifies a GitHub OAuth
callback URL on cantelop.com as unsafe under the deceptive-content category.
The screenshot includes OAuth query parameters; they are deliberately not
copied here. The exact classifier trigger is unknown. Search Console showed
“No issues detected” after domain verification, while the bare-domain public
lookup returned “No available data”. Neither overrides the positive result
for the specific URL. A possible-false-positive report was submitted and
Google confirmed receipt; that is not a clearance.

This review covers public landing/docs/blog content, the sign-in interface,
auth configuration and cookie code, live unauthenticated auth responses on
the three console domains, the current public installer source, and release
workflow configuration. It is not an exhaustive vulnerability scan, deployed
binary inspection, infrastructure audit, or certification of Google's approval.
Production callback execution, browser cookie delivery at the blocked request,
Google Cloud consent configuration, storage access controls, and actual data
retention still need verification.

## Policy checks

| Area | Evidence | Assessment / remaining work |
| --- | --- | --- |
| Deceptive login / impersonation | Cantelop has its own visible brand; provider buttons send users to provider OAuth. No provider-password input is present. Callback uses NextAuth. | No phishing behavior found in inspected code. Added an explanation of provider handoff and account data. This does not establish the reason Google flagged the URL. |
| Third-party content | Retrieved landing, quick-start, and blog HTML showed same-origin Next.js scripts and no iframes. Source includes Vercel Analytics and provider logos/images. | No ad network or deceptive embedded content found in this sample. Server HTML does not enumerate every dynamically loaded resource or conditional response. Analytics must be included in privacy disclosures. |
| Privacy policy | No privacy route or public policy link found in source or sampled live pages. Account identity/email are persisted; profile information is carried in the session. | **Confirmed gap for public Google Sign-In.** Publish an accurate policy, link it prominently from the home/sign-in pages, and register it in Google OAuth configuration. Operator, contact, processing/sharing, retention, backups and deletion procedures require owner confirmation. |
| Identity, home page, scopes | Public home page describes agent hosting. GitHub app homepage and .com callbacks are correct. Google provider requests `openid email profile`; GitHub requests `read:user user:email`. | No Gmail, Drive or repository-content scopes in the inspected login configuration. Google Cloud branding, support contact, verified domains, production/test separation and actual configured clients remain unverified. Terms of service are optional in Google's cited homepage guidance. |
| Secure redirects and tokens | Live provider URLs and GitHub sign-in requests stay on their starting HTTPS domain. Same GitHub client ID on all three hosts. Secure host-only HttpOnly/Lax cookies, 900-second state, provider-bound encrypted state. | No .com-specific mismatch found. Added `private, no-store` and `no-referrer` to auth responses locally. Before this change, sign-in returned `public, max-age=0, must-revalidate` on all hosts; observed responses were cache misses. Not evidence of a flag trigger. |
| Installation disclosures | Public installer downloads a pinned release over HTTPS, checks a platform SHA-256, runs the downloaded CLI's version command, installs/replaces a binary and can edit a shell profile. | Existing quick-start did not explain those side effects. Updated it to disclose installation effects, save the script for inspection, and disable profile edits in the documented invocation. The published installer itself is unchanged. |
| Removal | Existing docs lacked uninstall guidance. | Added Homebrew/manual removal and reversal of older installer PATH edits. Local credentials/projects and hosted resources are separate from the executable; documented this distinction. |
| Publisher / binary integrity | Release workflows require macOS signing, notarization and verification; installer checks archive hashes. | Pipeline evidence only. Actual distributed binary signatures, notarization and Linux publisher-verification coverage were not independently validated. A checksum alone is not publisher authentication. |
| Operational handling of user data | Identity database, session cookies, telemetry and hosted workspaces exist. | Actual retention/deletion, backups, infrastructure encryption, staff access and third-party processing have not been verified. Do not publish promises about these without confirming operations. |

## Policy sources

- [Google social engineering policy](https://developers.google.com/search/docs/monitor-debug/security/social-engineering): deceptive identity/content and embedded content are covered; it recommends clear provider identity and conventional OAuth, and describes review/reporting options.
- [Google API Services User Data Policy](https://developers.google.com/terms/api-services-user-data-policy): applies to Google Sign-In, requires an accessible and accurate privacy policy, minimum relevant permissions and protected user data.
- [Google OAuth production guidance](https://developers.google.com/identity/protocols/oauth2/production-readiness/policy-compliance): covers public homepage/privacy link, verified owned domains, accurate branding, secure callbacks and production configuration.
- [Google Unwanted Software Policy](https://www.google.com/about/unwanted-software-policy.html): covers clear installation choices, system-change and data disclosures, publisher verification, removal and expected behavior.

Google OAuth/privacy requirements are separate from Safe Browsing's deceptive
content classification. A privacy-policy gap is real but has not been shown
to cause the GitHub-only warning. The missing `no-store` header and installation
disclosure gaps likewise must not be presented as proven causes.

## Changes and validation

Local changes: auth cache/referrer headers; sign-in data disclosure; installer
inspection and opt-out instructions; uninstall documentation. Not deployed.
TypeScript type checking passed; the domain/auth and installer redirect tests
passed (10 tests). Earlier diagnostic checks confirmed state-cookie generation,
decryption, provider binding, clearing and separate Set-Cookie serialization
for all three domains. These checks do not complete a real production login.

## Before claiming compliance

1. Confirm operator, contact and actual data practices; publish and register the
   privacy policy and link it in the public interface.
2. Verify Google OAuth branding, domains, support contact, client scopes and
   production/test separation in the owning Cloud project.
3. Deploy and verify the local changes, including rendered sign-in/docs pages.
4. Verify production data protections, deletion/backup behavior and distributed
   release artifacts. Inspect any additional URLs Google reports.
5. Obtain Google's review outcome and retest a fresh GitHub login on .com.
   Do not rename the callback or disable browser protection to evade the flag.

## Follow-up — 2026-09-29

Search Console now reports one “Deceptive pages” security issue for the
`cantelop.com` domain property, with no sample URL. Google Safe Browsing's
public site-status check says that some pages are unsafe; its displayed data
was last updated September 22. These signals do not identify the offending
page or prove that the previously flagged GitHub callback is still the cause.

The `cantelop.com` and `www.cantelop.com` production domains are assigned to
the Vercel deployment of `main` at `ac70c942`, which includes the auth response
cache and referrer headers added in `d5ff5109`. A normal GitHub sign-in on
September 29 returned through `/api/auth/callback/github` with a 302 and
reached `/profile` without a browser warning. This one successful flow cannot
clear a context-dependent Safe Browsing classification.

The Search Console property has two verified owners (the operator's
`stepan@cantelop.com` and `stepan.arsentjev@gmail.com`) and zero unused
ownership tokens. A sample of the live homepage, blog, and sign-in code showed
no deceptive content or injected third-party UI. Vercel project domains list
only `cantelop.com`, `www.cantelop.com`, `console.cantelop.dev`, and its
Vercel-hosted alias. These checks are limited to the inspected surfaces; the
exact flagged URL remains unknown.

The public Google Sign-In privacy-policy gap is being addressed separately
with a full `/privacy` page and a clear link from sign-in. It was not shown to
cause the Safe Browsing issue. Search Console's review form requires an
“All issues were fixed” declaration. Do not submit it until the current issue
has been identified or the operator is confident after the site-wide check
that there is no deceptive content; the review explanation must say exactly
what was checked or changed.
