// SPDX-License-Identifier: MIT
// The template source and emitted Cantelop starter content use licenses/MIT-EXAMPLES.txt.
package main

const projectInitAPITemplate = `import { CantelopClient } from "@cantelop/sdk";
import { receive } from "./agent.js";

const cantelop = new CantelopClient();
export const agent = cantelop.app<{ prompt: string }>({
  name: "__APP_SLUG__",
  runtime: { receive },
  environment: __ENVIRONMENT__,
});
`

var projectInitProviderTemplates = map[string]string{
	"openai": `import { Codex } from "@openai/codex-sdk";
import type { SessionContext } from "@cantelop/sdk/session";

interface AgentMessage {
  prompt: string;
}

const codex = new Codex();
const thread = codex.startThread({
  workingDirectory: process.cwd(),
  skipGitRepoCheck: true,
  sandboxMode: "workspace-write",
});

export async function receive({ message }: SessionContext<AgentMessage>) {
  if (typeof message.payload?.prompt !== "string" || !message.payload.prompt.trim()) {
    throw new TypeError("prompt is required");
  }
  await thread.run(message.payload.prompt);
};
`,
	"claude": `import { query } from "@anthropic-ai/claude-agent-sdk";
import type { SessionContext } from "@cantelop/sdk/session";

interface AgentMessage {
  prompt: string;
}

let sessionId: string | undefined;

export async function receive({ message }: SessionContext<AgentMessage>) {
  if (typeof message.payload?.prompt !== "string" || !message.payload.prompt.trim()) {
    throw new TypeError("prompt is required");
  }
  for await (const event of query({
    prompt: message.payload.prompt,
    options: {
      cwd: process.cwd(),
      ...(sessionId ? { resume: sessionId } : {}),
      permissionMode: "bypassPermissions",
      allowDangerouslySkipPermissions: true,
    },
  })) {
    if ("session_id" in event && typeof event.session_id === "string") {
      sessionId = event.session_id;
    }
  }
};
`,
	"pi": `import type { SessionContext } from "@cantelop/sdk/session";
import {
  createAgentSession,
  ModelRuntime,
  SessionManager,
} from "@earendil-works/pi-coding-agent";

interface AgentMessage {
  prompt: string;
}

const agent = (async () => {
  const modelRuntime = await ModelRuntime.create();
  return createAgentSession({
    cwd: process.cwd(),
    modelRuntime,
    sessionManager: SessionManager.create(process.cwd()),
  });
})();

export async function receive({ message }: SessionContext<AgentMessage>) {
  if (typeof message.payload?.prompt !== "string" || !message.payload.prompt.trim()) {
    throw new TypeError("prompt is required");
  }
  const { session } = await agent;
  await session.prompt(message.payload.prompt);
};
`,
}
