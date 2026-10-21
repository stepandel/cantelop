import type { Metadata } from "next";
import Link from "next/link";

import { SiteFooter } from "@/components/site-footer";
import { SiteTopbar } from "@/components/site-topbar";
import styles from "./privacy.module.css";

export const metadata: Metadata = {
  title: "Privacy policy",
  description: "How Cantelop handles account, service, and website data.",
  alternates: { canonical: "https://cantelop.com/privacy" },
  robots: { index: true, follow: true },
};

export default function PrivacyPolicy() {
  return <main className="site">
    <SiteTopbar />
    <article className={styles.article}>
      <h1>Privacy policy</h1>
      <p className={styles.updated}>Last updated October 19, 2026</p>

      <p>Cantelop is operated by Arsent LLC, 600 California St., San Francisco, CA. This policy explains how we handle information when you visit cantelop.com, sign in, or use our hosted agent runtimes. Contact us at <a href="mailto:privacy@cantelop.com">privacy@cantelop.com</a> with privacy questions or requests.</p>

      <h2>Information we handle</h2>
      <ul>
        <li><strong>Account information.</strong> When you sign in with GitHub or Google, we receive your provider account ID, verified email address, name, and profile image. We use these to identify your account, display your profile, and control access. We do not receive your provider password. Google sign-in requests basic identity scopes (<code>openid</code>, <code>email</code>, and <code>profile</code>); GitHub sign-in requests basic profile and email access.</li>
        <li><strong>Service content.</strong> We process the Apps, configurations, code, files, agent inputs and outputs, and Workspace content you choose to place in Cantelop so we can run your agents and provide the service. Configurations may include secrets you supply. Other people you authorize as App admins may access that App under its permissions.</li>
        <li><strong>Usage and technical information.</strong> We process request and diagnostic logs, resource usage, deployment and runtime status, and website analytics to operate, secure, troubleshoot, and improve Cantelop. Service providers may also receive technical information such as IP addresses and browser details when you use the site.</li>
        <li><strong>Invitations.</strong> If someone invites you to an App, we use your email address to send the invitation and track delivery and suppression preferences. Our <Link href="/invitations/privacy">invitation privacy notice</Link> explains this feature in more detail.</li>
        <li><strong>Contact messages.</strong> If you use the <Link href="/contact">contact form</Link>, we receive your name (if given), email address, and message by email so we can reply.</li>
      </ul>

      <h2>How we use and share information</h2>
      <p>We use account information to authenticate you and provide access to Cantelop. We use service content to perform the operations you request. We use technical information to maintain service reliability, investigate failures or abuse, and understand website use. We do not sell account or Workspace content or use Google sign-in data for advertising.</p>
      <p>We use service providers to run Cantelop, including Vercel for the website and analytics, Neon for product data storage, Hetzner for compute infrastructure, Cloudflare for network services, and Resend for invitation and contact form email. GitHub or Google handles the authentication step when you choose that provider. We may disclose information when required by law or to protect the service and its users.</p>

      <h2>Storage and deletion</h2>
      <p>Account and service information is retained while needed to provide the service, maintain security and records, or meet legal obligations. You can delete supported resources in the product and ask us to access, correct, or delete personal information by emailing <a href="mailto:privacy@cantelop.com">privacy@cantelop.com</a>. Deletion from live systems may not immediately remove residual copies from backups; those copies remain until the backups expire. We will explain any applicable limits when responding to a request.</p>
      <p>Invitation records and delivery metadata are eligible for deletion 90 days after an invitation expires. We retain email-address fingerprints used to honor opt-outs and prevent repeated failed deliveries. The <Link href="/invitations/privacy">invitation privacy notice</Link> has more detail.</p>

      <h2>Security and changes</h2>
      <p>We use access controls and other technical measures to protect the information we process. No service can guarantee absolute security. We may update this policy as the service changes and will update the date above when we do.</p>
    </article>
    <SiteFooter />
  </main>;
}
