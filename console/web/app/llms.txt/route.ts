import { docsTextResponse, llmsIndex } from "../../lib/docs-markdown";

export const dynamic = "force-static";

export function GET() {
  return docsTextResponse(llmsIndex(), "text/plain");
}
