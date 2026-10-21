import { Axis, Connector, Figure, Label, Node } from "@/components/figure/figure";

/* The durable Session, the replaceable Sandbox it activates, and the durable
   Workspace that Sandbox mounts, drawn as three lanes over time so the
   difference in lifetime is the picture. */

export function SandboxModelDiagram() {
  return (
    <Figure
      width={600}
      height={308}
      label="Three lanes over a time axis. The Session lane is one continuous bar with a stable identity and mailbox. It activates a Sandbox, a bounded segment on the Sandbox lane holding agent processes, memory, and temporary files, followed by a dashed slot for the next Sandbox. The Workspace lane is one continuous bar of durable files that the Sandbox mounts at /workspace."
      caption="The Session is durable. The Sandbox is replaceable. Workspace files outlive both process memory and temporary storage."
    >
      <Label x={16} y={49} anchor="start" kind="name">Session</Label>
      <Label x={16} y={141} anchor="start" kind="name">Sandbox</Label>
      <Label x={16} y={233} anchor="start" kind="name">Workspace</Label>

      <Node x={156} y={24} w={428} h={40} kind="subject" />
      <Label x={172} y={49} anchor="start">one ID and one mailbox, the whole time</Label>

      <Connector from={[196, 64]} to={[196, 112]} dashed label="activates" />

      <Node x={172} y={112} w={240} h={48} title="Sandbox" />
      <Node x={428} y={112} w={156} h={48} kind="absent" title="next Sandbox" />

      <Connector from={[292, 160]} to={[292, 208]} heads="both" label="/workspace" />

      <Node x={156} y={208} w={428} h={40} kind="durable" />
      <Label x={172} y={233} anchor="start">same files under every Sandbox · optionally shared</Label>

      <Axis x1={156} x2={584} y={276} />
    </Figure>
  );
}
