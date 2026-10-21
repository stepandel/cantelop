import { CantelopClient } from "@cantelop/sdk";
import { receive } from "./agent.js";

const cantelop = new CantelopClient();
export const hello = cantelop.app<
  { message: string },
  { message: string },
  { message: string }
>({
  name: "hello",
  runtime: { receive },
  homepage: () => new Response("Cantelop hello App"),
});
