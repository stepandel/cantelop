import { Connector, Context, Figure, Label, Node } from "@/components/figure/figure";

/* The home page's overview: two users, each on their own device, reach one
   App; the App routes each to that user's own Workspace, and each Workspace
   is mounted by the Sandboxes that run that user's agent. Drawn left to right
   so the fan-out is the picture. */

/* The two users' rows; everything on a user's path sits on its row. */
const A = 170;
const B = 390;
/* Where the App's two paths split. */
const SPLIT = 440;

/* Device glyphs in the figure's one stroke weight, white on the ground like
   a component box, so they read as part of the same drawing. */
function Laptop({ cx, cy }: { cx: number; cy: number }) {
  return (
    <g fill="var(--surface)" stroke="var(--ink)" strokeWidth={2} strokeLinejoin="round">
      <rect x={cx - 40} y={cy - 32} width={80} height={54} rx={5} />
      <rect x={cx - 32} y={cy - 24} width={64} height={38} rx={2} fill="var(--fig-panel)" strokeWidth={1.5} />
      <path d={`M${cx - 50},${cy + 22} H${cx + 50} L${cx + 46},${cy + 30} H${cx - 46} Z`} />
    </g>
  );
}

function Phone({ cx, cy }: { cx: number; cy: number }) {
  return (
    <g fill="var(--surface)" stroke="var(--ink)" strokeWidth={2} strokeLinejoin="round">
      <rect x={cx - 22} y={cy - 38} width={44} height={76} rx={8} />
      <rect x={cx - 16} y={cy - 28} width={32} height={52} rx={2} fill="var(--fig-panel)" strokeWidth={1.5} />
      <line x1={cx - 5} y1={cy + 31} x2={cx + 5} y2={cy + 31} strokeLinecap="round" />
    </g>
  );
}

export function AgentPerUserDiagram() {
  return (
    <Figure
      width={960}
      height={520}
      label="Two users, one on a laptop and one on a phone, both connect to your app. Your app routes each user to their own Workspace: one for the laptop user and one for the phone user. The laptop user's Workspace is mounted by two Sandboxes and the phone user's by one, each Sandbox isolated from the others."
    >
      <Laptop cx={80} cy={A - 4} />
      <Label x={80} y={A + 54}>user A</Label>
      <Phone cx={80} cy={B} />
      <Label x={80} y={B + 62}>user B</Label>

      <Connector from={[138, A]} via={[[200, A], [200, 268]]} to={[260, 268]} />
      <Connector from={[110, B]} via={[[200, B], [200, 292]]} to={[260, 292]} />

      <Node x={260} y={244} w={140} h={72} kind="subject" title="Your app" />

      <Connector from={[400, 268]} via={[[SPLIT, 268], [SPLIT, A]]} to={[480, A]} />
      <Connector from={[400, 292]} via={[[SPLIT, 292], [SPLIT, B]]} to={[480, B]} />

      <Node x={480} y={A - 30} w={196} h={60} kind="durable" title="Workspace (user A)" />
      <Node x={480} y={B - 30} w={196} h={60} kind="durable" title="Workspace (user B)" />

      <Context x={716} y={60} w={220} h={420} label="scalable agent runtime">
        <Connector from={[676, A]} via={[[700, A], [700, 125]]} to={[746, 125]} ground="panel" />
        <Connector from={[676, A]} via={[[700, A], [700, 215]]} to={[746, 215]} ground="panel" />
        <Connector from={[676, B]} to={[746, B]} ground="panel" />
        <Node x={746} y={101} w={160} h={48} title="Sandbox" />
        <Node x={746} y={191} w={160} h={48} title="Sandbox" />
        <Node x={746} y={B - 24} w={160} h={48} title="Sandbox" />
      </Context>
    </Figure>
  );
}
