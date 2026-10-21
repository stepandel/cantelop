export const metadata = { title: "Invitation privacy", referrer: "no-referrer" };
export default function Page() {
  return <main className="invitation-landing"><article>
    <h1>Invitation privacy</h1>
    <p>An app owner supplied your email address to invite you to manage their app on Cantelop. We use it to send that invitation through Resend and to enforce sending limits. Receiving an invitation does not subscribe you to marketing.</p>
    <p>Invitations expire after seven days. Invitation records and delivery-event metadata are eligible for daily deletion 90 days after expiry to support access troubleshooting and abuse prevention. We do not store message bodies in our delivery-event log.</p>
    <p>You can stop future invitation emails using the link in any invitation. We retain a fingerprint of the address and the suppression reason to honor that preference or prevent repeated bounces and complaints. Fingerprints are still treated as personal data.</p>
    <p>Opting out does not change existing app access. To ask about your data, request deletion, or re-enable invitations, contact <a href="mailto:privacy@cantelop.com">privacy@cantelop.com</a>.</p>
  </article></main>;
}
