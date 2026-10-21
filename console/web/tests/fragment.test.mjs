import { afterEach, expect, test } from "bun:test";

import { fragmentHref, navigateToFragment } from "../lib/fragment.ts";

// The page the user is on, as far as the helper can tell. A hash the browser
// is told to set lands here too, so a test can see which way a navigation went.
function onPage(pathname) {
  globalThis.window = { location: { pathname, hash: "" } };
}

afterEach(() => {
  delete globalThis.window;
});

test("a fragment href splits into its page and its hash", () => {
  expect(fragmentHref("/profile#example")).toEqual({ path: "/profile", hash: "example" });
  expect(fragmentHref("/profile")).toBeNull();
});

test("on the fragment's own page the browser moves, so hashchange fires", () => {
  onPage("/profile");
  const pushed = [];
  navigateToFragment({ push: (href) => pushed.push(href) }, "/profile#example");
  expect(window.location.hash).toBe("example");
  expect(pushed).toEqual([]);
});

test("from any other page, and for plain routes, the router moves", () => {
  onPage("/apps/app_1");
  const pushed = [];
  const router = { push: (href) => pushed.push(href) };
  navigateToFragment(router, "/profile#new-app");
  navigateToFragment(router, "/profile");
  expect(pushed).toEqual(["/profile#new-app", "/profile"]);
  expect(window.location.hash).toBe("");
});
