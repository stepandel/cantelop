import { Axis, Connector, Context, Figure, Label, Node } from "@/components/figure/figure";

import styles from "./diagrams.module.css";

/* The post's three figures, drawn with the shared figure grammar. */

export function PlatformOverviewDiagram() {
  return (
    <Figure
      width={600}
      height={352}
      className={styles.lead}
      label="Clients send HTTP requests to the Edge API, which dispatches messages to Session A and Session B. Each Session has a mailbox that feeds its own Sandbox, where the Session SDK runs. Both Sandboxes mount the same shared Workspace at /workspace."
      caption="Platform architecture"
    >
      <Node x={40} y={20} w={128} h={60} title="Clients" />
      <Connector from={[168, 50]} to={[260, 50]} label="HTTP" />
      <Node x={260} y={20} w={128} h={60} title="Edge API" />

      <Connector from={[304, 80]} via={[[304, 102], [154, 102]]} to={[154, 128]} label="dispatch" />
      <Connector from={[344, 80]} via={[[344, 102], [446, 102]]} to={[446, 128]} label="dispatch" />

      <Context x={16} y={128} w={276} h={112} label="Session A">
        <Node x={36} y={164} w={88} h={56} title="Mailbox" />
        <Connector from={[124, 192]} to={[168, 192]} />
        <Node x={168} y={164} w={104} h={56} kind="subject" title="Sandbox A" />
      </Context>

      <Context x={308} y={128} w={276} h={112} label="Session B">
        <Node x={328} y={164} w={88} h={56} title="Mailbox" />
        <Connector from={[416, 192]} to={[460, 192]} />
        <Node x={460} y={164} w={104} h={56} kind="subject" title="Sandbox B" />
      </Context>

      <Connector from={[220, 220]} to={[220, 280]} heads="both" label="/workspace" />
      <Connector from={[512, 220]} to={[512, 280]} heads="both" label="/workspace" />

      <Node x={16} y={280} w={568} h={56} kind="durable" title="Workspace" />
    </Figure>
  );
}

export function SessionActorDiagram() {
  return (
    <Figure
      width={600}
      height={264}
      label="The Edge API routes requests by Session ID and dispatches each as a message into the Session actor's FIFO mailbox, drawn as a queue of three messages. The mailbox feeds ordered intake to the Session runtime; the harness controls agent work while remaining responsive to input."
      caption="Intake is ordered. The harness controls work; different Sessions run concurrently."
    >
      <Node x={16} y={112} w={96} h={56} title="Edge API" />

      <Context x={192} y={16} w={392} h={232} label="Session actor · one ID">
        <Node x={212} y={52} w={128} h={176} align="top" title="FIFO mailbox">
          <Node x={228} y={92} w={96} h={32} title="msg 1" />
          <Node x={228} y={132} w={96} h={32} title="msg 2" />
          <Node x={228} y={172} w={96} h={32} title="msg 3" />
        </Node>
        <Connector from={[340, 140]} to={[416, 140]} />
        <Label x={378} y={164}>one at</Label>
        <Label x={378} y={180}>a time</Label>
        <Node x={416} y={52} w={152} h={176} kind="subject" title="Session runtime" />
      </Context>
      <Connector from={[112, 140]} to={[212, 140]} label="dispatch" />
    </Figure>
  );
}

export function SandboxLifecycleDiagram() {
  return (
    <Figure
      width={600}
      height={324}
      label="Three lanes over a time axis. The Session lane is one continuous bar with the ID ses_123. The Sandbox lane holds Sandbox A, allocated at message 1 and reused at message 2, then a gap where the keep-alive expires and A ends, then Sandbox B allocated at message 3. The Workspace lane is one continuous bar, mounted at /workspace by both Sandboxes."
      caption="The Session ID and the Workspace outlive any one Sandbox."
    >
      <Label x={16} y={81} anchor="start" kind="name">Session</Label>
      <Label x={16} y={165} anchor="start" kind="name">Sandbox</Label>
      <Label x={16} y={249} anchor="start" kind="name">Workspace</Label>

      <Label x={180} y={16}>message 1</Label>
      <Connector from={[180, 24]} to={[180, 56]} />
      <Label x={290} y={16}>message 2</Label>
      <Connector from={[290, 24]} to={[290, 56]} />
      <Label x={500} y={16}>message 3</Label>
      <Connector from={[500, 24]} to={[500, 56]} />

      <Node x={140} y={56} w={444} h={40} kind="subject" />
      <Label x={568} y={81} anchor="end">one ID the whole time</Label>

      <Connector from={[180, 96]} to={[180, 140]} dashed label="allocate A" />
      <Connector from={[290, 96]} to={[290, 140]} dashed label="reuse A" />
      <Connector from={[500, 96]} to={[500, 140]} dashed label="allocate B" />

      <Node x={150} y={140} w={170} h={40} title="Sandbox A" />
      <Label x={390} y={156}>keep-alive expires</Label>
      <Connector from={[328, 168]} to={[452, 168]} heads="none" dashed tone="quiet" />
      <Node x={460} y={140} w={124} h={40} title="Sandbox B" />

      <Connector from={[235, 180]} to={[235, 224]} heads="both" label="/workspace" />
      <Connector from={[500, 180]} to={[500, 224]} heads="both" label="/workspace" />

      <Node x={140} y={224} w={444} h={40} kind="durable" />
      <Label x={568} y={249} anchor="end">same files under A and B</Label>

      <Axis x1={140} x2={584} y={292} />
    </Figure>
  );
}
