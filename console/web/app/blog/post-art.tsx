import type { ReactNode } from "react";

import { SandboxLifecycleDiagram } from "./(posts)/foundational-principles/diagrams";
import { CantelopClaudeCodeDiagram } from "./(posts)/hosting-claude-code-bring-your-own-subscription/diagrams";
import { SandboxBoundariesDiagram } from "./(posts)/running-agents-as-untrusted-code/diagrams";
import type { Post } from "./posts";

/* Each post card's visual band, shared by the blog index and the home page.
   A post supplies a figure of its own; one without an entry here shows the
   band's ground alone. */
export const POST_ART: Partial<Record<Post["slug"], () => ReactNode>> = {
  "hosting-claude-code-bring-your-own-subscription": () => <CantelopClaudeCodeDiagram />,
  "running-agents-as-untrusted-code": () => <SandboxBoundariesDiagram />,
  "foundational-principles": () => <SandboxLifecycleDiagram />,
};
