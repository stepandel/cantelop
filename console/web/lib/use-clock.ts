"use client";

import { useSyncExternalStore } from "react";

import { clockFor, type Clock } from "@/lib/format";

// The viewer's time zone is a browser fact. The server pass has no way to
// resolve it, and drawing one zone into the HTML and another into the
// hydration would be a mismatch React repairs at random. useSyncExternalStore
// is the supported way out: the server — and the hydration render that has to
// match it — get UTC, and the browser's own zone arrives in a re-render
// immediately after. Nothing subscribes, because a zone does not change under
// a live page.
const subscribe = () => () => {};
const browserZone = () => Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
const serverZone = () => "UTC";

// Every component that draws a timestamp reads its formatters from here — a
// component that imports them from lib/format directly would be left showing
// UTC, since only this hook re-renders it once the zone is known.
export function useClock(): Clock {
  return clockFor(useSyncExternalStore(subscribe, browserZone, serverZone));
}
