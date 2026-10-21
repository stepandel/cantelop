/* The blog's table of contents. A post is `app/blog/(posts)/<slug>/page.mdx`;
   its entry here is what the index lists and what the post's own header
   shows; the summary is the one line the home page sets under the title.
   Newest first. */

export const AUTHORS = {
  stepandel: {
    name: "Stepan Arsentjev",
    url: "https://github.com/stepandel",
    avatar: "https://avatars.githubusercontent.com/u/20438019?s=80&v=4",
  },
} as const;

export type Author = (typeof AUTHORS)[keyof typeof AUTHORS];

export const POSTS = [
  {
    slug: "hosting-claude-code-bring-your-own-subscription",
    title: "Hosting Claude Code with bring-your-own-subscription",
    date: "2026-09-24",
    summary: "Users sign in with their own Claude account and the agent runs on their plan — what that takes on Cantelop.",
    author: AUTHORS.stepandel,
  },
  {
    slug: "running-agents-as-untrusted-code",
    title: "Running AI agents as untrusted code",
    date: "2026-09-09",
    summary: "Compute, host, network, resource, and lifecycle boundaries: how a sandbox keeps an agent useful and contained.",
    author: AUTHORS.stepandel,
  },
  {
    slug: "foundational-principles",
    title: "Cantelop",
    date: "2026-09-03",
    summary: "Minimal setup, any harness, Sessions as actors, and performance on the critical path.",
    author: AUTHORS.stepandel,
  },
] as const;

export type Post = (typeof POSTS)[number];
