import { Connector, Figure, Node } from "@/components/figure/figure";

export function AgentWorkflow() {
  return (
    <Figure width={480} height={288}
      label="A GitHub webhook calls the API, which messages a Session running the OpenCode harness."
      caption="One session per conversation. Each turn runs the agent and streams its progress back.">
      <Node x={120} y={12} w={240} h={56} title="GitHub webhook" />
      <Connector from={[240, 68]} to={[240, 116]} label="Request" />
      <Node x={120} y={116} w={240} h={56} title="API" />
      <Connector from={[240, 172]} to={[240, 220]} label="Message" />
      <Node x={120} y={220} w={240} h={56} kind="subject" title="Session (OpenCode harness)" />
    </Figure>
  );
}
