import { highlight } from "@/components/markdown/highlight";
import { Snippet } from "@/components/markdown/snippet";

/* The scaling panel's picture: the body of a GET events route in the
   customer's own backend — select the user's Workspace, select the Session
   they are talking to on it, and stream its output back. Messages reach that
   Session through session.dispatch() in a separate route; without the
   Session's id, session() would name a fresh, silent Session. The same calls
   as the SDK 1.0 README's backend examples. */
const CODE = `const session = app
  .workspace({ slug: userId })
  .session({ id: sessionId, keepAliveSeconds: 300 });

for await (const event of session.stream({ signal })) {
  send(event.data);
}`;

export async function ParallelSessionsSnippet() {
  const lines = await highlight(CODE, "ts", false);
  return <Snippet code={CODE} lines={lines} title="GET /events" />;
}
