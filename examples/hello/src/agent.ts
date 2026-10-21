import type { SessionContext } from "@cantelop/sdk/session";

export interface EchoMessage {
  message: string;
}
export interface EchoEvent {
  message: string;
}

export async function receive({
  message,
  output,
  reply,
}: SessionContext<EchoMessage, EchoEvent, EchoEvent>) {
  if (
    typeof message.payload?.message !== "string" ||
    !message.payload.message.length
  ) {
    throw new TypeError("message is required");
  }
  const result = { message: message.payload.message };
  await output.send(result);
  reply(result);
}
