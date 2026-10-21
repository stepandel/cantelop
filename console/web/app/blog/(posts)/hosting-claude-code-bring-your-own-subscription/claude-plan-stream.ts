import { ClaudeSessionOutput, type ClaudeSessionEvent } from "@cantelop/sdk/harnesses/claude/protocol";

const TERMINAL_STATUSES = new Set(["completed", "failed", "cancelled", "steered", "interrupted"]);

export type RunState =
  | { kind: "running"; status: string; response: string }
  | { kind: "completed"; response: string }
  | { kind: "failed"; message: string };
type StreamEvent = {
  type?: string; id?: string; status?: string; code?: string; event?: unknown; eventId?: string;
  index?: number; total?: number; json?: string;
};

export async function followStream(body: ReadableStream<Uint8Array>, messageId: string, signal: AbortSignal, update: (run: RunState) => void) {
  const reply = new ReplyText(); const output = new ClaudeSessionOutput(messageId);
  const terminal = await readSSE(body, (payload) => {
    if ((payload.type === "claude" || payload.type === "claude.fragment") && payload.id === messageId) {
      const native = output.accept(payload as ClaudeSessionEvent);
      if (native) reply.apply(native);
    }
    if (payload.type === "auth.required" && payload.id === messageId) { update({ kind: "failed", message: "Claude sign-in expired. Reconnect and try again." }); return true; }
    if (payload.type === "error") { update({ kind: "failed", message: `The Claude session reported ${payload.code ?? "an error"}.` }); return true; }
    if (payload.type === "message.status" && payload.id === messageId) {
      if (payload.status === "running") update({ kind: "running", status: reply.status, response: reply.text });
      else if (payload.status === "queued") update({ kind: "running", status: "Message queued", response: reply.text });
      else if (payload.status === "completed") { update({ kind: "completed", response: reply.text }); return true; }
      else if (payload.status && TERMINAL_STATUSES.has(payload.status)) { update({ kind: "failed", message: `Claude Code ${payload.status}.` }); return true; }
    } else if ((payload.type === "claude" || payload.type === "claude.fragment") && payload.id === messageId) update({ kind: "running", status: reply.status, response: reply.text });
    return false;
  }, signal);
  if (!terminal) throw new Error("The Claude event stream ended before the message finished.");
}

// Builds the reply from Claude Code's stream-json output: text deltas stream in while a
// message is generated, complete assistant messages replace them, and tool calls set the status.
class ReplyText {
  private done: string[] = [];
  private draft = "";
  private result = "";
  // Until Claude Code emits its first event, the Sandbox and CLI are still starting.
  status = "Starting Claude Code";

  get text() { return [...this.done, this.draft].filter(Boolean).join("\n\n") || this.result; }

  apply(value: unknown) {
    const event = record(value);
    if (!event) return;
    if (event.type === "stream_event") {
      const inner = record(event.event), delta = record(inner?.delta), block = record(inner?.content_block);
      if (inner?.type === "message_start") { this.draft = ""; this.status = "Claude is thinking"; }
      else if (delta?.type === "thinking_delta") this.status = "Claude is thinking";
      else if (delta?.type === "text_delta" && typeof delta.text === "string") { this.draft += delta.text; this.status = "Claude Code is writing"; }
      else if (block?.type === "tool_use" && typeof block.name === "string") this.status = `Using ${block.name}`;
    } else if (event.type === "assistant") {
      const content = record(event.message)?.content;
      if (!Array.isArray(content)) return;
      const blocks = content.map(record);
      const text = blocks.flatMap((block) => block?.type === "text" && typeof block.text === "string" ? [block.text] : []).join("\n");
      if (text) this.done.push(text);
      this.draft = "";
      const tool = blocks.findLast((block) => block?.type === "tool_use");
      if (tool) this.status = describeTool(tool);
    } else if (event.type === "system" || event.type === "user") {
      this.status = "Claude is thinking";
    } else if (event.type === "result" && typeof event.result === "string") {
      this.result = event.result;
    }
  }
}

function describeTool(tool: Record<string, unknown>): string {
  const input = record(tool.input), path = typeof input?.file_path === "string" ? input.file_path.split("/").pop() : undefined;
  if (tool.name === "Write") return path ? `Writing ${path}` : "Writing a file";
  if (tool.name === "Edit") return path ? `Editing ${path}` : "Editing a file";
  if (tool.name === "Read") return path ? `Reading ${path}` : "Reading a file";
  if (tool.name === "Glob" || tool.name === "Grep") return "Searching the workspace";
  return typeof tool.name === "string" ? `Using ${tool.name}` : "Claude Code is working";
}

function record(value: unknown): Record<string, unknown> | undefined {
  return value && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : undefined;
}

async function readSSE(body: ReadableStream<Uint8Array>, onPayload: (payload: StreamEvent) => boolean | Promise<boolean>, signal: AbortSignal): Promise<boolean> {
  const reader = body.getReader(); const decoder = new TextDecoder(); let buffer = "";
  try {
    while (!signal.aborted) {
      const chunk = await reader.read(); buffer += decoder.decode(chunk.value, { stream: !chunk.done });
      let match: RegExpExecArray | null;
      while ((match = /\r?\n\r?\n/.exec(buffer))) {
        const frame = buffer.slice(0, match.index); buffer = buffer.slice(match.index + match[0].length);
        const serialized = frame.split(/\r?\n/).filter((line) => line.startsWith("data:")).map((line) => line.slice(5).trimStart()).join("\n");
        if (serialized) {
          const wire = JSON.parse(serialized) as unknown;
          const data = wire && typeof wire === "object" && !Array.isArray(wire) ? (wire as Record<string, unknown>).data : undefined;
          const payload = data && typeof data === "object" && !Array.isArray(data)
            ? (wire as Record<string, unknown>).data as StreamEvent
            : wire as StreamEvent;
          if (await onPayload(payload)) return true;
        }
      }
      if (chunk.done) return false;
    }
    return false;
  } finally { await reader.cancel().catch(() => undefined); }
}
