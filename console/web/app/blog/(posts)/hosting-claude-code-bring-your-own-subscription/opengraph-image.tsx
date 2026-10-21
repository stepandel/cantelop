import { figureImage } from "../../og";
import { CantelopClaudeCodeDiagram } from "./diagrams";

export { contentType, size } from "../../og";

/* The post's social image: the figure its card on the index carries, drawn
   by og.tsx from the post's own diagrams. */

export const alt =
  "A client sends a task to the Cantelop API. Cantelop routes a message to one of that user's many ephemeral sandboxes running Claude Code. Each user's sandboxes all mount that user's one durable /workspace, which contains .credentials.json. Claude Code returns events to the client over MCP.";

export default function Image() {
  return figureImage(<CantelopClaudeCodeDiagram />);
}
