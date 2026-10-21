/* What shipped, newest first, since the first official release on Sep 3.
   An entry is one capability a user can reach, not a commit: name it the way
   the docs would, and say in one sentence what it lets someone do. Say what it
   does, never what it is built on — the front door names no underlying
   technology. An SDK entry carries the version it shipped in. Text in `code`
   is set in the typed face. */

export type ChangelogEntry = {
  date: string;
  title: string;
  summary: string;
  /* The @cantelop/sdk version that shipped it, for SDK changes. */
  sdk?: string;
};

export const CHANGELOG: readonly ChangelogEntry[] = [
  {
    date: "2026-10-14",
    title: "Typed Apps",
    summary: "Define an App in code and call its Sessions from a typed backend client, with the same App running locally and in production.",
    sdk: "1.0",
  },
  {
    date: "2026-10-14",
    title: "SDK command traces",
    summary: "See traffic, errors, and latency for each SDK command, then open a request to follow its message, sandbox, and logs.",
  },
  {
    date: "2026-10-12",
    title: "Faster Session activation",
    summary: "First-time Workspace activation fell from 3.46 s to 0.99 s median in sequential benchmarks, measured through Session handler entry.",
  },
  {
    date: "2026-09-29",
    title: "Fully elastic sandboxes",
    summary: "Sandboxes grow and shrink with what the agent is doing, so you never size them up front.",
  },
  {
    date: "2026-09-22",
    title: "Stop a Session's sandbox",
    summary: "`session.stop()` releases the current sandbox right away; the Session stays reusable.",
    sdk: "0.12",
  },
  {
    date: "2026-09-21",
    title: "Platform observability upgrade",
    summary: "Every API route gets its own page: traffic and errors beside the outcome of the Sessions it started.",
  },
  {
    date: "2026-09-21",
    title: "Synchronous messages",
    summary: "`session.request()` sends a message and waits for the Session's reply, beside fire-and-forget `dispatch()`.",
    sdk: "0.11",
  },
  {
    date: "2026-09-21",
    title: "Route discovery",
    summary: "`router.list()` enumerates an API's routes, and each deployment publishes them for the console.",
    sdk: "0.10",
  },
  {
    date: "2026-09-16",
    title: "Session recovery",
    summary: "An opt-in `onRecover` hook resumes interrupted work on a replacement sandbox with the same workspace.",
    sdk: "0.9",
  },
  {
    date: "2026-09-11",
    title: "OpenCode harness",
    summary: "Run OpenCode inside a Cantelop Session alongside Claude Code, Codex, and Pi.",
    sdk: "0.8.1",
  },
  {
    date: "2026-09-03",
    title: "First official release",
    summary: "Cantelop opens to builders, with SDK 0.8 opening a Session straight from a workspace slug.",
  },
];
