"use client";
import { useState, type FormEvent } from "react";

import styles from "@/app/contact/contact.module.css";
import topbar from "./site-topbar.module.css";

const errors: Record<string, string> = {
  invalid_message: "Check your email address and message, then try again.",
  rate_limited: "Too many messages from this connection. Try again in an hour.",
};

export function ContactForm() {
  const [status, setStatus] = useState<"ready" | "sending" | "sent">("ready");
  const [error, setError] = useState<string | null>(null);

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setStatus("sending");
    setError(null);
    try {
      const response = await fetch("/api/contact", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(Object.fromEntries(new FormData(event.currentTarget))),
      });
      if (response.ok) { setStatus("sent"); return; }
      const body = await response.json().catch(() => ({})) as { error?: string };
      setError(errors[body.error ?? ""] ?? "Your message could not be sent. Try again in a few minutes.");
    } catch {
      setError("Your message could not be sent. Check your connection and try again.");
    }
    setStatus("ready");
  }

  if (status === "sent") {
    return <div className={styles.sent} role="status">
      <p>Thanks — your message is on its way.</p>
      <p>We’ll reply to the address you gave.</p>
    </div>;
  }
  return (
    <form className={styles.form} onSubmit={(event) => void submit(event)}>
      <label className={styles.field}>
        <span>Name <span className={styles.optional}>(optional)</span></span>
        <input name="name" autoComplete="name" maxLength={100} />
      </label>
      <label className={styles.field}>
        Email
        <input name="email" type="email" autoComplete="email" maxLength={254} required />
      </label>
      <label className={styles.field}>
        Message
        <textarea name="message" maxLength={5000} required />
      </label>
      <label className={styles.trap} aria-hidden="true">
        Website
        <input name="website" tabIndex={-1} autoComplete="off" />
      </label>
      <div className={styles.actions}>
        <button className={topbar.cta} type="submit" disabled={status === "sending"}>
          {status === "sending" ? "Sending…" : "Send message"}
        </button>
        {error && <p className={styles.error} role="alert">{error}</p>}
      </div>
    </form>
  );
}
