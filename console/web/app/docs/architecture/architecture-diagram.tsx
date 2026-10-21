import { Connector, Context, Figure, Node } from "@/components/figure/figure";

/* One command's path: your backend, the App's generated Edge Worker, the
   Session actor it lands on
   inside a Sandbox, and the Workspace that outlives that Sandbox. The actor
   is the subject; the Sandbox is the context it runs in. */

export function ArchitectureDiagram() {
  return (
    <Figure
      width={600}
      height={336}
      label="Your backend sends a command through the SDK to the App's generated Edge Worker, which dispatches a message to a Session actor running inside a Sandbox. The actor's mailbox feeds its agent runtime, which can run a managed activity. The Sandbox reads and writes a Workspace that persists outside its lifecycle."
      caption="Releasing the Sandbox clears memory. The Session ID and Workspace survive."
    >
      <Node x={16} y={28} w={128} h={56} title="Your backend" />
      <Connector from={[80, 84]} to={[80, 128]} label="SDK" />
      <Node x={16} y={128} w={128} h={56} title="Edge Worker" />

      <Context x={268} y={16} w={316} h={212} label="Sandbox · per activation">
        <Node x={288} y={48} w={276} h={160} kind="subject" align="top" title="Session actor">
          <Node x={304} y={124} w={80} h={64} title="Mailbox" />
          <Connector from={[384, 156]} to={[416, 156]} />
          <Node x={416} y={124} w={132} h={64} title="Agent runtime" />
        </Node>
      </Context>
      <Connector from={[144, 156]} to={[288, 156]} label="dispatch" />

      <Connector from={[426, 228]} to={[426, 268]} heads="both" />
      <Node x={268} y={268} w={316} h={52} kind="durable" title="Workspace" />
    </Figure>
  );
}
