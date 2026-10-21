import { Connector, Context, Figure, Label, Node } from "@/components/figure/figure";

/* The post's figures. The last two share one layout so they read as a
   before and after: the client and the control plane on the left, fanning
   out to a panel per user on the right. */

/* A panel per user, and where the control plane's fan splits toward them. */
const USERS = ["user A", "user B"];
const PANEL = 376;
const COLUMN = 388;
const FAN = 356;

export function SingleUserClaudeCodeDiagram() {
  return (
    <Figure
      width={600}
      height={316}
      label="Step 1: the client calls /auth on Claude Code running in a sandbox, and Claude Code returns an auth URL. Step 2: after the user signs in, the client passes the one-time sign-in code through to Claude Code, which completes Anthropic's sign-in and stores the credentials in ~/.claude/.credentials.json."
    >
      <Label x={16} y={36} anchor="start" kind="name">1.</Label>
      <Node x={16} y={56} w={120} h={64} title="Client" />
      <Context x={240} y={20} w={160} h={120} label="sandbox">
        <Node x={260} y={56} w={120} h={64} kind="subject" title="Claude Code" />
      </Context>
      <Connector from={[136, 76]} to={[260, 76]} label="/auth" />
      <Connector from={[260, 100]} to={[136, 100]} label="auth url" />

      <Label x={16} y={196} anchor="start" kind="name">2.</Label>
      <Node x={16} y={216} w={120} h={64} title="Client" />
      <Context x={240} y={180} w={344} h={120} label="sandbox">
        <Node x={260} y={216} w={120} h={64} kind="subject" title="Claude Code" />
        <Connector from={[380, 248]} to={[412, 248]} />
        <Node x={412} y={216} w={152} h={64} kind="durable" title=".credentials.json" />
      </Context>
      <Connector from={[136, 248]} to={[260, 248]} label="sign-in code" />
    </Figure>
  );
}

/* Before: each user's panel holds one long-lived sandbox, and the
   workspace lives inside it. */
export function HostedClaudeCodeDiagram() {
  const top = (i: number) => 16 + i * 156;
  return (
    <Figure
      width={600}
      height={328}
      label="A client sends a task to a control plane. The control plane routes a message to that user's sandbox running Claude Code; each user has one sandbox, which holds that user's /workspace. Claude Code returns events to the client over MCP."
    >
      <Node x={16} y={142} w={80} h={64} title="Client" />
      <Connector from={[96, 174]} to={[176, 174]} label="task" />
      <Node x={176} y={142} w={120} h={64} title="Control plane" />

      {USERS.map((user, i) => (
        <Context key={user} x={PANEL} y={top(i)} w={208} h={140} label={user}>
          <Node x={COLUMN} y={top(i) + 36} w={176} h={88} kind="subject" align="top" title="Claude Code sandbox">
            <Node x={COLUMN + 16} y={top(i) + 82} w={144} h={30} kind="durable" title="/workspace" />
          </Node>
        </Context>
      ))}
      <Connector from={[296, 174]} to={[FAN, 174]} heads="none" label="message" />
      {USERS.map((user, i) => (
        <Connector key={user} from={[FAN, 174]} via={[[FAN, top(i) + 80]]} to={[COLUMN, top(i) + 80]} />
      ))}

      <Connector from={[COLUMN, 272]} via={[[56, 272]]} to={[56, 206]} label="MCP" />
    </Figure>
  );
}

/* After: each user's panel holds a stack of ephemeral sandboxes, all
   mounting the one durable workspace beneath them. */
const STACK = 8;

export function CantelopClaudeCodeDiagram() {
  const top = (i: number) => 16 + i * 188;
  const front = (i: number) => top(i) + 32 + STACK;
  return (
    <Figure
      width={600}
      height={392}
      label="A client sends a task to the Cantelop API. Cantelop routes a message to one of that user's many ephemeral sandboxes running Claude Code. Each user's sandboxes all mount that user's one durable /workspace, which contains .credentials.json. Claude Code returns events to the client over MCP."
      caption="Cantelop separates many ephemeral Sessions from one durable per-user Workspace."
    >
      <Node x={16} y={144} w={80} h={64} title="Client" />
      <Connector from={[96, 176]} to={[176, 176]} label="task" />
      <Node x={176} y={144} w={120} h={64} kind="subject" title="Cantelop API" />

      {USERS.map((user, i) => (
        <Context key={user} x={PANEL} y={top(i)} w={208} h={172} label={user}>
          {/* The card behind says there are more; the ground under the front
              card keeps the one behind from showing through its dashes. */}
          <Node x={COLUMN + STACK} y={front(i) - STACK} w={176} h={52} kind="absent" />
          <rect x={COLUMN} y={front(i)} width={176} height={52} rx={12} fill="var(--fig-panel)" />
          <Node x={COLUMN} y={front(i)} w={176} h={52} kind="absent" title="Claude Code sandbox" />
          <Connector from={[COLUMN + 88, front(i) + 52]} to={[COLUMN + 88, front(i) + 72]} />
          <Node x={COLUMN} y={front(i) + 72} w={176} h={44} kind="durable" title="/workspace" />
        </Context>
      ))}
      <Connector from={[296, 176]} to={[FAN, 176]} heads="none" label="message" />
      {USERS.map((user, i) => (
        <Connector key={user} from={[FAN, 176]} via={[[FAN, front(i) + 26]]} to={[COLUMN, front(i) + 26]} />
      ))}

      <Connector from={[COLUMN, 286]} via={[[56, 286]]} to={[56, 208]} label="MCP" />
    </Figure>
  );
}
