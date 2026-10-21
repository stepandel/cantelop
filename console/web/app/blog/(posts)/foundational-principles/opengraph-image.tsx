import { figureImage } from "../../og";
import { SandboxLifecycleDiagram } from "./diagrams";

export { contentType, size } from "../../og";

/* The post's social image: the figure its card on the index carries, drawn
   by og.tsx from the post's own diagrams. */

export const alt =
  "Three lanes over a time axis. The Session lane is one continuous bar with the ID ses_123. The Sandbox lane holds Sandbox A, allocated at message 1 and reused at message 2, then a gap where the keep-alive expires and A ends, then Sandbox B allocated at message 3. The Workspace lane is one continuous bar, mounted at /workspace by both Sandboxes.";

export default function Image() {
  return figureImage(<SandboxLifecycleDiagram />);
}
