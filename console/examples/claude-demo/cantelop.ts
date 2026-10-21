import { CantelopClient } from "@cantelop/sdk";
import { claudeAuth, claudeDockerfile, createClaudeRuntime } from "@cantelop/sdk/harnesses/claude";
import type { ClaudeSessionCommand, ClaudeSessionEvent, ClaudeSessionReply } from "@cantelop/sdk/harnesses/claude/protocol";

export const app = new CantelopClient().app<ClaudeSessionCommand, ClaudeSessionEvent, ClaudeSessionReply>({
  name: "cantelop-claude-api",
  runtime: createClaudeRuntime({
    permissionMode: "dontAsk",
    settingSources: [],
    maxTurns: 8,
    tools: ["Read", "Write", "Edit", "Glob", "Grep"],
    allowedTools: ["Read", "Write", "Edit", "Glob", "Grep"],
    mcps: {},
  }),
  auth: claudeAuth,
  dockerfile: claudeDockerfile,
});
