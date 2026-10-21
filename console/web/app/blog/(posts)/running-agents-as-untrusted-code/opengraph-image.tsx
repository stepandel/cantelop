import { figureImage } from "../../og";
import { SandboxBoundariesDiagram } from "./diagrams";

export { contentType, size } from "../../og";

/* The post's social image: the figure its card on the index carries, drawn
   by og.tsx from the post's own diagrams. */

export const alt =
  "Five concentric layers around one agent process, from the outside in. Lifecycle, drawn dashed and marked ephemeral. Network: the host firewall, wrapping everything inside it. Host containment: the jail. Resources: memory and vCPUs. Compute: the Firecracker microVM, holding the agent process at the center.";

export default function Image() {
  return figureImage(<SandboxBoundariesDiagram />);
}
