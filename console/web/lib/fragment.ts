import type { MouseEvent } from "react";

/* Navigating to a fragment on the page already open — /profile#example from
   /profile — is where Next's router goes quiet: it pushes the new URL but
   nothing fires for a hash-only change, so the page's hashchange listener,
   which is what raises the create form and the example dialog, never hears
   it. From any other page the router lands on the page with the hash in
   place, and the page reads it on mount. So: on the page itself, move the
   way the browser does, which does fire the event; from anywhere else, the
   router's way. */
export function fragmentHref(href: string) {
  const at = href.indexOf("#");
  return at === -1 ? null : { path: href.slice(0, at), hash: href.slice(at + 1) };
}

export function navigateToFragment(router: { push: (href: string) => void }, href: string) {
  const fragment = fragmentHref(href);
  if (fragment && fragment.path === window.location.pathname) {
    window.location.hash = fragment.hash;
  } else {
    router.push(href);
  }
}

/* The same rule for a <Link> to a fragment: let the router take the click
   unless the link is on its own page already, where the browser must. */
export function fragmentLinkClick(event: MouseEvent<HTMLAnchorElement>) {
  const fragment = fragmentHref(event.currentTarget.getAttribute("href") ?? "");
  if (!fragment || fragment.path !== window.location.pathname) return;
  event.preventDefault();
  window.location.hash = fragment.hash;
}
