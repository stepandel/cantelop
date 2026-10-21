export type Guide = {
  slug: string;
  title: string;
  description: string;
  eyebrow: string;
  repository: string;
};

export const CLAUDE_CODE_GUIDE: Guide = {
  slug: "claude-code",
  title: "Host Claude Code for your users",
  description: "Run unmodified Claude Code in a Sandbox per Session, with each user signed in to their own Claude subscription and a durable Workspace that keeps them signed in.",
  eyebrow: "TypeScript · Claude Code · Subscription sign-in",
  repository: "https://github.com/stepandel/claude-code-api",
};

export const GUIDES = [CLAUDE_CODE_GUIDE];
