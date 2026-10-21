import { docsTextResponse, llmsFullText } from "../../lib/docs-markdown";

export const dynamic = "force-static";

export function GET() {
  return docsTextResponse(llmsFullText(), "text/plain");
}
