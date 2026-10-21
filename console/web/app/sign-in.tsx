"use client";

import { useEffect, useRef, useState, type ReactNode } from "react";
import { signIn } from "next-auth/react";

import { IDENTITY_PROVIDERS, type IdentityProvider } from "@core/auth/providers";
import { ProviderMark } from "@/components/provider-marks";
import styles from "./home.module.css";
import dialogStyles from "./sign-in.module.css";

/* Sign-in starts straight from the home page — there is no /signin route.
   Protected pages bounce here with ?callbackUrl=…, and NextAuth reports OAuth
   failures here with ?error=…, so both are read from the address bar. Reading
   it at click/mount time keeps the page static.

   Every way in — Get started, Sign in, the footer — is one button that opens
   the same dialog, where the person picks GitHub or Google. */

const PROVIDER_NAMES: Record<IdentityProvider, string> = { github: "GitHub", google: "Google" };

export function providerName(provider: IdentityProvider): string {
  return PROVIDER_NAMES[provider];
}

function requestedCallbackUrl() {
  const requested = new URLSearchParams(window.location.search).get("callbackUrl");
  // The CLI authorize flow hands over an absolute callbackUrl; anything not
  // ours falls back to the console.
  return requested && (requested.startsWith("/") || requested.startsWith(`${window.location.origin}/`))
    ? requested
    : "/profile";
}

/* Which provider this browser last signed in with. It is what lets a bounce
   from a protected page carry straight on to the right provider instead of
   stopping to ask; it is a convenience, so its absence is just the dialog. */
const REMEMBERED_PROVIDER_KEY = "cantelop.sign-in.provider";

function rememberedProvider(): IdentityProvider | null {
  try {
    const stored = window.localStorage.getItem(REMEMBERED_PROVIDER_KEY);
    return (IDENTITY_PROVIDERS as readonly string[]).includes(stored ?? "") ? (stored as IdentityProvider) : null;
  } catch {
    return null;
  }
}

function startSignIn(provider: IdentityProvider) {
  try {
    window.localStorage.setItem(REMEMBERED_PROVIDER_KEY, provider);
  } catch {
    // Private windows and blocked storage: the sign-in still proceeds.
  }
  void signIn(provider, { callbackUrl: requestedCallbackUrl() });
}

/* The dialog is mounted once, by the topbar, and opened from anywhere on the
   page by an event on window — the openers and the dialog share no ancestor
   short of the page, and a context for one boolean would be more machinery
   than the thing it carries. */
const OPEN_EVENT = "cantelop:sign-in";

function openSignIn() {
  window.dispatchEvent(new Event(OPEN_EVENT));
}

/** A way in: opens the provider dialog. Styled entirely by the caller. */
export function SignInButton({ className, children }: { className: string; children: ReactNode }) {
  return (
    <button type="button" className={className} onClick={openSignIn}>
      {children}
    </button>
  );
}

/** The choice of provider. A native dialog: modal, Escape closes it, focus is
 *  trapped and returned for free. */
export function SignInDialog() {
  const ref = useRef<HTMLDialogElement>(null);
  const [pending, setPending] = useState<IdentityProvider | null>(null);

  useEffect(() => {
    const open = () => {
      if (!ref.current?.open) ref.current?.showModal();
    };
    window.addEventListener(OPEN_EVENT, open);
    return () => window.removeEventListener(OPEN_EVENT, open);
  }, []);

  return (
    <dialog
      ref={ref}
      className={dialogStyles.dialog}
      aria-labelledby="sign-in-title"
      // The dialog box itself has no padding, so a click that lands on the
      // element rather than a child is a click on the backdrop. Once a
      // redirect is under way there is nothing to go back to, so neither the
      // backdrop nor Escape closes it.
      onClick={(event) => {
        if (event.target === event.currentTarget && !pending) event.currentTarget.close();
      }}
      onCancel={(event) => {
        if (pending) event.preventDefault();
      }}
      onKeyDown={(event) => {
        if (event.key !== "Escape") return;
        event.preventDefault();
        if (!pending) event.currentTarget.close();
      }}
    >
      <div className={dialogStyles.body}>
        <h2 id="sign-in-title" className={dialogStyles.title}>Sign in to Cantelop</h2>
        <div className={dialogStyles.providers}>
          {IDENTITY_PROVIDERS.map((provider) => (
            <button
              key={provider}
              type="button"
              className={dialogStyles.provider}
              disabled={pending !== null}
              onClick={() => {
                setPending(provider);
                startSignIn(provider);
              }}
            >
              <ProviderMark provider={provider} />
              {pending === provider
                ? `Redirecting to ${providerName(provider)}…`
                : `Continue with ${providerName(provider)}`}
            </button>
          ))}
        </div>
        <button
          type="button"
          className={dialogStyles.close}
          disabled={pending !== null}
          onClick={() => ref.current?.close()}
        >
          Cancel
        </button>
      </div>
    </dialog>
  );
}

/* A callbackUrl in the address bar means someone was already on their way to
   a protected page. When this browser has said which provider it uses, start
   the OAuth flow without asking for another click; otherwise open the dialog
   so the only click left is the choice. An error means the last attempt
   failed — stay put and let the notice below explain, instead of bouncing
   straight back to the provider. */
export function AutoSignIn() {
  useEffect(() => {
    const params = new URLSearchParams(window.location.search);
    if (!params.get("callbackUrl") || params.get("error")) return;
    const provider = rememberedProvider();
    if (provider) {
      startSignIn(provider);
      return;
    }
    // The dialog's listener is attached in a sibling's effect that runs after
    // this one; let the tick end so it is there to hear the event.
    const timer = window.setTimeout(openSignIn, 0);
    return () => window.clearTimeout(timer);
  }, []);

  return null;
}

const ERROR_COPY: Record<string, string> = {
  AccessDenied: "That account can't sign in to this console. It needs an email address its provider has verified.",
  OAuthSignin: "Couldn't reach the sign-in provider to start sign-in. Try again.",
  OAuthCallback: "The sign-in provider didn't complete the sign-in. Try again.",
  OAuthAccountNotLinked: "That email is already linked to a different account.",
  SessionRequired: "Sign in to view that page.",
  Callback: "Sign-in was interrupted. Try again.",
};

export function SignInErrorNotice() {
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    setError(new URLSearchParams(window.location.search).get("error"));
  }, []);

  if (!error) return null;
  return (
    <p className={styles.authError} role="alert">
      {ERROR_COPY[error] ?? "Something went wrong during sign-in. Try again."}
    </p>
  );
}
