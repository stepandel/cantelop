import type { Metadata } from "next";
import type { ReactNode } from "react";
import { Geist, Geist_Mono, Sora } from "next/font/google";
import { Analytics } from "@vercel/analytics/next";

import "./globals.css";

// Three faces. Geist is what a person reads — labels, navigation, headings,
// table text, prose. Geist Mono is reserved for what is literally typed or
// pasted: code, commands, identifiers, digests, log lines. Both variable, so
// no weight list; the stylesheets use 400 and 500 of each. Sora is the brand
// face and sets exactly one word, the lowercase wordmark, at Medium.
const sans = Geist({ subsets: ["latin"], variable: "--font-sans" });
const brand = Sora({ subsets: ["latin"], weight: "500", variable: "--font-brand" });
const mono = Geist_Mono({
  subsets: ["latin"],
  variable: "--font-mono",
  // The generated fallback is size-adjusted Arial — a proportional face
  // standing in for a monospace one during every swap window. Disable it so
  // the metric-close monospace tail in --mono is reachable instead.
  adjustFontFallback: false,
});

export const metadata: Metadata = {
  // Link previews use the stable public control origin. Browser auth uses the
  // domain of the current request.
  metadataBase: new URL(process.env.CANTELOP_CONTROL_ORIGIN?.trim() || process.env.NEXTAUTH_URL?.trim() || "https://console.cantelop.dev"),
  title: { default: "Cantelop console", template: "%s · Cantelop" },
  description: "The Cantelop console — apps, workspaces, and live runtime telemetry.",
  robots: { index: false, follow: false },
};

export default function RootLayout({ children }: Readonly<{ children: ReactNode }>) {
  return (
    <html lang="en" className={`${mono.variable} ${sans.variable} ${brand.variable}`}>
      <body>
        {children}
        <Analytics />
      </body>
    </html>
  );
}
