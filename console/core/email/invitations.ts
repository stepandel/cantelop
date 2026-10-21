import { optOutToken } from "./preferences";
import type { PreparedInvitation } from "../db/invitation-repository";

function escapeHTML(text: string) {
  return text.replace(/[&<>"']/g, (character) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[character]!);
}

export function invitationEmail(prepared: PreparedInvitation, origin: string, preferenceToken?: string) {
  const url = new URL(origin);
  if (url.username || url.password || url.pathname !== "/" || url.search || url.hash ||
      (url.protocol !== "https:" && !(url.protocol === "http:" && ["localhost", "127.0.0.1"].includes(url.hostname)))) {
    throw new Error("Invitation origin must be an HTTPS origin (or loopback for development)");
  }
  // Fragments never reach HTTP access logs or referrer headers. The acceptance
  // page keeps the token in sessionStorage while OAuth navigates away.
  const link = `${url.origin}/invitations/accept#token=${prepared.token}`;
  const intro = `${prepared.inviterEmail} invited you to join Cantelop and collaborate on the project “${prepared.appSlug}”.`;
  const instructions = `Continue with Google or GitHub using ${prepared.invitation.email}, then accept the invitation. You’ll get admin access to the project. This link expires in seven days.`;
  const preferenceLink = preferenceToken ? `${url.origin}/invitations/preferences#token=${preferenceToken}` : null;
  const footer = "You received this email because a project owner invited you to collaborate on Cantelop. If you weren’t expecting this invitation, you can ignore it or stop future invitation emails.";
  return {
    subject: `Join Cantelop to collaborate on ${prepared.appSlug}`,
    text: `${intro}\n\n${instructions}\n\nAccept invitation: ${link}\n\n${footer}${preferenceLink ? `\nStop invitation emails: ${preferenceLink}` : ""}\nInvitation privacy: ${url.origin}/invitations/privacy`,
    // Tables, inline styles, and a PNG keep the branding usable in email clients.
    // The wordmark and all invitation details remain readable with images blocked.
    html: `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>You’re invited to Cantelop</title></head>
<body style="margin:0;padding:0;background-color:#EBE8E1;color:#0B2E22;font-family:Arial,Helvetica,sans-serif">
<table role="presentation" width="100%" cellspacing="0" cellpadding="0" border="0" bgcolor="#EBE8E1"><tr><td align="center" style="padding:32px 16px">
<!--[if mso]><table role="presentation" width="560" cellspacing="0" cellpadding="0" border="0"><tr><td><![endif]-->
<table role="presentation" width="100%" cellspacing="0" cellpadding="0" border="0" style="max-width:560px">
<tr><td style="padding:0 0 24px"><table role="presentation" cellspacing="0" cellpadding="0" border="0"><tr>
<td width="48"><img src="${url.origin}/email/cantelop-mark.png" width="40" height="40" alt="" style="display:block;border:0"></td>
<td style="font-size:24px;font-weight:700;letter-spacing:-1px;color:#0B2E22">Cantelop</td>
</tr></table></td></tr>
<tr><td bgcolor="#F9F8F4" style="padding:32px 24px;border:1px solid #D5D0C5;border-top:3px solid #DF7A2E;border-radius:4px;overflow-wrap:anywhere;word-break:break-word">
<p style="margin:0 0 16px;font-size:12px;line-height:18px;letter-spacing:1.5px;color:#476052;font-weight:700">YOU’RE INVITED</p>
<h1 style="margin:0 0 20px;font-size:28px;line-height:36px;font-weight:600;letter-spacing:-0.7px;color:#0B2E22">You’re invited to Cantelop.</h1>
<p style="margin:0 0 16px;font-size:16px;line-height:26px;color:#1F3F33">${escapeHTML(intro)}</p>
<p style="margin:0 0 24px;font-size:15px;line-height:25px;color:#1F3F33">Continue with Google or GitHub using <strong>${escapeHTML(prepared.invitation.email)}</strong>, then accept the invitation.</p>
<table role="presentation" cellspacing="0" cellpadding="0" border="0"><tr><td bgcolor="#0B2E22" style="border-radius:4px;mso-padding-alt:14px 24px"><a href="${escapeHTML(link)}" style="display:inline-block;padding:14px 24px;border:1px solid #0B2E22;border-radius:4px;color:#FFFFFF;font-size:15px;line-height:20px;font-weight:700;text-decoration:none">Accept invitation</a></td></tr></table>
<p style="margin:20px 0 0;font-size:13px;line-height:21px;color:#526257">You’ll get admin access to the project. This link expires in seven days.</p>
</td></tr>
<tr><td style="padding:24px 4px 0;font-size:12px;line-height:20px;color:#526257">
<p style="margin:0 0 12px">${escapeHTML(footer)}</p>
<p style="margin:0">${preferenceLink ? `<a href="${escapeHTML(preferenceLink)}" style="color:#365443;text-decoration:underline">Stop invitation emails</a><span style="padding:0 8px">&middot;</span>` : ""}<a href="${url.origin}/invitations/privacy" style="color:#365443;text-decoration:underline">Invitation privacy</a></p>
</td></tr></table>
<!--[if mso]></td></tr></table><![endif]-->
</td></tr></table></body></html>`,
  };
}

export async function sendInvitationEmail(
  prepared: PreparedInvitation,
  environment: NodeJS.ProcessEnv = process.env,
  fetcher: typeof fetch = fetch,
): Promise<string> {
  const key = environment.CANTELOP_INVITATION_RESEND_API_KEY?.trim() || environment.RESEND_API_KEY?.trim();
  const from = environment.CANTELOP_INVITATION_FROM?.trim();
  const origin = environment.CANTELOP_INVITATION_ORIGIN?.trim() || environment.CANTELOP_CONTROL_ORIGIN?.trim();
  if (!key || !from || !origin) throw new Error("Invitation email is not configured");
  const token = optOutToken(prepared.invitation.email, environment);
  const email = invitationEmail(prepared, origin, token);
  const replyTo = environment.CANTELOP_INVITATION_REPLY_TO?.trim();
  if (!replyTo || !/^[^\s@<>]+@[^\s@<>]+\.[^\s@<>]+$/.test(replyTo)) throw new Error("Invitation reply address is not configured");
  const response = await fetcher("https://api.resend.com/emails", {
    method: "POST",
    headers: {
      Authorization: `Bearer ${key}`,
      "Content-Type": "application/json",
      "Idempotency-Key": `app-invitation/${prepared.invitation.id}`,
    },
    body: JSON.stringify({ from, to: [prepared.invitation.email], reply_to: replyTo, ...email,
      tags: [{ name: "invitation_id", value: prepared.invitation.id }],
      headers: {
        "List-Unsubscribe": `<${new URL(origin).origin}/api/invitations/unsubscribe?token=${token}>`,
        "List-Unsubscribe-Post": "List-Unsubscribe=One-Click",
      },
    }),
    signal: AbortSignal.timeout(10_000),
    redirect: "error",
  });
  if (!response.ok) throw new Error(`Invitation email provider rejected the request (HTTP ${response.status})`);
  const result = await response.json() as { id?: unknown };
  if (typeof result.id !== "string" || !result.id) throw new Error("Invitation email provider returned an invalid receipt");
  return result.id;
}
