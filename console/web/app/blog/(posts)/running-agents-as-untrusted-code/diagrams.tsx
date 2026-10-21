import { Connector, Context, Figure, Funnel, Label, Node, Wall } from "@/components/figure/figure";

import styles from "./diagrams.module.css";

/* The post's figures, drawn with the shared figure grammar. The overview
   places all five boundaries on one Sandbox; each later figure draws the
   claim its caption makes: what passes, what is bounded, what is shared,
   what never comes back. */

/* One layer of the overview: a ring with its title at the top left, drawn
   in whichever shape the grammar gives that kind of thing. */
function Layer({ x, y, title }: { x: number; y: number; title: string }) {
  return <Label x={x + 16} y={y + 24} anchor="start" kind="name">{title}</Label>;
}

export function SandboxBoundariesDiagram() {
  return (
    <Figure
      width={600}
      height={328}
      className={styles.lead}
      label="Five concentric layers around one agent process, from the outside in. Lifecycle, drawn dashed and marked ephemeral. Network: the host firewall, wrapping everything inside it. Host containment: the jail. Resources: memory and vCPUs. Compute: the Firecracker microVM, holding the agent process at the center."
      caption="Five boundaries around one agent process."
    >
      <Node x={8} y={8} w={584} h={312} kind="absent" />
      <Layer x={8} y={8} title="Lifecycle · ephemeral" />

      <Wall x={26} y={44} w={548} h={258} label="" />
      <Layer x={26} y={44} title="Network · host firewall" />

      <Context x={44} y={80} w={512} h={204} label="">
        <Layer x={44} y={80} title="Host containment · jail" />

        <Node x={62} y={116} w={476} h={150} />
        <Layer x={62} y={116} title="Resources · memory and vCPUs" />

        <Node x={80} y={152} w={440} h={96} kind="subject" />
        <Layer x={80} y={152} title="Compute · Firecracker microVM" />
        <Node x={228} y={186} w={144} h={44} title="agent process" />
      </Context>
    </Figure>
  );
}

export function NetworkBoundaryDiagram() {
  const rows = [48, 110, 172, 234, 296];
  const mid = rows.map((y) => y + 24);
  return (
    <Figure
      width={600}
      height={372}
      label="A Sandbox on the left initiates three connections that pass through the host firewall: to public destinations, to the App gateway for HTTPS callbacks, and to authorized Workspace storage over NFS. Its attempt to reach private and metadata addresses stops at the firewall, and an unsolicited inbound connection from the right stops at the firewall too."
      caption="Cantelop applies a stateful host firewall."
    >
      <Node x={16} y={48} w={112} h={296} kind="subject" title="Sandbox" />
      <Wall x={172} y={32} w={16} h={328} label="Host firewall" />

      <Connector from={[128, mid[0]]} to={[300, mid[0]]} label="egress" />
      <Node x={300} y={rows[0]} w={284} h={48} title="Public destinations" />

      <Connector from={[128, mid[1]]} to={[300, mid[1]]} label="HTTPS" />
      <Node x={300} y={rows[1]} w={284} h={48} title="App gateway" />

      <Connector from={[128, mid[2]]} to={[300, mid[2]]} label="NFS" />
      <Node x={300} y={rows[2]} w={284} h={48} title="Workspace storage" />

      <Connector from={[128, mid[3]]} to={[172, mid[3]]} blocked />
      <Label x={200} y={mid[3] + 5} anchor="start" red>blocked</Label>
      <Node x={300} y={rows[3]} w={284} h={48} kind="absent" title="Private and metadata addresses" />

      <Connector from={[300, mid[4]]} to={[188, mid[4]]} blocked />
      <Label x={200} y={mid[4] - 10} anchor="start" red>blocked</Label>
      <Node x={300} y={rows[4]} w={284} h={48} kind="absent" title="Unsolicited inbound" />
    </Figure>
  );
}

export function ResourceBoundaryDiagram() {
  return (
    <Figure
      width={600}
      height={232}
      label="Inside a physical host, Sandbox A and Sandbox B have configured guest memory and vCPU counts. Both use the same shared pool of physical CPU and memory, without reserved physical cores."
      caption="Guest memory and vCPU count are bounded. Physical host resources remain shared."
    >
      <Context x={16} y={16} w={568} h={200} label="Physical host">
        <Funnel top={[40, 288]} bottom={[64, 200]} y1={116} y2={148} />
        <Funnel top={[312, 560]} bottom={[336, 484]} y1={116} y2={148} />
        <Node x={40} y={56} w={248} h={60} title="Sandbox A" />
        <Node x={312} y={56} w={248} h={60} title="Sandbox B" />

        <Node x={40} y={148} w={520} h={48} kind="solid" title="One shared pool of physical CPU and memory" />
      </Context>
    </Figure>
  );
}

export function WorkspaceBoundaryDiagram() {
  return (
    <Figure
      width={600}
      height={276}
      label="App A contains Session A1 and Session A2, each in its own Sandbox, both attached to Workspace A. App B contains Session B1 in its own Sandbox, attached to Workspace B. A path from Session A2 toward App B stops at the App boundary: no attachment crosses it."
      caption="Intended Workspace access model: sharing within an App, no attachments across App boundaries."
    >
      <Context x={16} y={16} w={268} h={244} label="App A">
        <Node x={32} y={52} w={112} h={56} title="Session A1" />
        <Node x={156} y={52} w={112} h={56} title="Session A2" />
        <Connector from={[88, 108]} to={[88, 164]} heads="both" />
        <Connector from={[212, 108]} to={[212, 164]} heads="both" />
        <Node x={32} y={164} w={236} h={56} kind="durable" title="Workspace A" />
        <Label x={150} y={244} red>attachment across Apps: blocked</Label>
      </Context>

      <Context x={316} y={16} w={268} h={244} label="App B">
        <Node x={332} y={52} w={112} h={56} title="Session B1" />
        <Connector from={[388, 108]} to={[388, 164]} heads="both" />
        <Node x={332} y={164} w={236} h={56} kind="durable" title="Workspace B" />
      </Context>

      <Connector from={[268, 80]} via={[[300, 80], [300, 192]]} to={[308, 192]} blocked />
    </Figure>
  );
}

export function SandboxDestructionDiagram() {
  /* Three stages, one per row, each showing the whole warm pool. The
     Sandbox's lane carries it from claimed, to destroyed, to gone; a fresh
     slot arrives on the far side. */
  const lanes = [40, 176, 312, 448];
  const rows = [16, 150, 284];
  const w = 112;
  const top = 36;
  const h = 48;
  const across = lanes[3] + w / 2;
  return (
    <Figure
      width={600}
      height={400}
      label="Three stages of the warm pool. First, activation: two slots are ready and one is claimed as Sandbox A. Second, termination: the same slot now holds Sandbox A destroyed, its VM stopped and jail removed. Third, replenishment: a fresh slot prepared after cleanup enters the pool on the left, and the used slot's place on the right is empty. A path from the destroyed Sandbox back into the pool is blocked."
      caption="A used Sandbox never returns to the warm pool. A fresh slot replaces it."
    >
      <Context x={16} y={rows[0]} w={568} h={100} label="1 · Activation">
        <Node x={lanes[1]} y={rows[0] + top} w={w} h={h} title="ready" />
        <Node x={lanes[2]} y={rows[0] + top} w={w} h={h} title="ready" />
        <Node x={lanes[3]} y={rows[0] + top} w={w} h={h} kind="subject" title="Sandbox A" />
      </Context>

      <Context x={16} y={rows[1]} w={568} h={100} label="2 · Termination">
        <Node x={lanes[1]} y={rows[1] + top} w={w} h={h} title="ready" />
        <Node x={lanes[2]} y={rows[1] + top} w={w} h={h} title="ready" />
        <Node x={lanes[3]} y={rows[1] + top} w={w} h={h} kind="absent" title="Sandbox A" />
      </Context>

      <Context x={16} y={rows[2]} w={568} h={100} label="3 · Replenishment">
        <Node x={lanes[0]} y={rows[2] + top} w={w} h={h} kind="absent" title="fresh slot" />
        <Node x={lanes[1]} y={rows[2] + top} w={w} h={h} title="ready" />
        <Node x={lanes[2]} y={rows[2] + top} w={w} h={h} title="ready" />
      </Context>

      {/* The transitions, drawn last so they sit over the panels. */}
      <Connector from={[across, rows[0] + top + h]} to={[across, rows[1] + top]} dashed />
      <Connector from={[across, rows[1] + top + h]} to={[across, rows[2] + top]} blocked />
      <Label x={across - 12} y={rows[2] + 24} anchor="end" red>never returns</Label>
    </Figure>
  );
}
