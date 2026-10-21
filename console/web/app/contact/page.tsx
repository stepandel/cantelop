import type { Metadata } from "next";

import { ContactForm } from "@/components/contact-form";
import { SiteFooter } from "@/components/site-footer";
import { SiteTopbar } from "@/components/site-topbar";
import styles from "./contact.module.css";

export const metadata: Metadata = {
  title: "Contact",
  description: "Send the Cantelop team a message.",
  alternates: { canonical: "https://cantelop.com/contact" },
  robots: { index: true, follow: true },
};

export default function Contact() {
  return <main className="site">
    <SiteTopbar />
    <article className={styles.article}>
      <h1>Contact us</h1>
      <p className={styles.lede}>Questions about Cantelop, a bug to report, or capacity you need during the beta? Send a message and we’ll reply by email.</p>
      <ContactForm />
    </article>
    <SiteFooter />
  </main>;
}
