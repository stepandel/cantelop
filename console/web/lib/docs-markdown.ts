import { markdownSources } from "../components/markdown/source";
import { configuredDocsOrigin } from "../../core/deployment";

export const DOCS_ORIGIN = configuredDocsOrigin();

// Read only public docs. These sources are prerendered with the text routes,
// so deployed handlers do not depend on MDX files being present at runtime.
export function docsMarkdown() {
  return Object.entries(markdownSources("web/app/docs", "/docs"))
    .sort(([a], [b]) => {
      if (a === "/docs/quick-start") return -1;
      if (b === "/docs/quick-start") return 1;
      return a.localeCompare(b);
    })
    .map(([path, markdown]) => ({
      path,
      title: /^# (.+)$/m.exec(markdown)?.[1] ?? path,
      markdown,
      url: `${configuredDocsOrigin()}${path}.md`,
    }));
}

export function llmsIndex() {
  return [
    "# Cantelop",
    "> Cantelop gives every user their own agent: fully elastic, isolated Sandboxes on a persistent per-user Workspace, running Claude Code, Codex, or your own harness, connected to any frontend with one SDK.",
    "SDK 1.0 is the only supported SDK; install it with npm install @cantelop/sdk. An App combines a generated Edge API with a Session runtime. These public docs cover the architecture, capabilities, and CLI workflows. See the SDK documentation for TypeScript implementation details.",
    "## Documentation",
    docsMarkdown().map(({ title, url }) => `- [${title}](${url})`).join("\n"),
    "## SDK",
    "- [SDK documentation](https://raw.githubusercontent.com/stepandel/cantelop-sdk/main/README.md): TypeScript APIs, transports, and runtime implementation.\n- [SDK examples](https://raw.githubusercontent.com/stepandel/cantelop-sdk/main/examples/README.md): Provider integrations and clients.",
    "## Optional",
    `- [All public docs](${configuredDocsOrigin()}/llms-full.txt): The full documentation in one file.`,
  ].join("\n\n") + "\n";
}

export function llmsFullText() {
  return "# Cantelop documentation\n\n" + docsMarkdown()
    .map(({ url, markdown }) => `Source: ${url}\n\n${markdown}`)
    .join("\n\n---\n\n") + "\n";
}

export function docsTextResponse(body: string, contentType = "text/markdown") {
  return new Response(body, {
    headers: {
      "Content-Type": `${contentType}; charset=utf-8`,
      "Link": '</llms.txt>; rel="describedby"',
      "X-Content-Type-Options": "nosniff",
    },
  });
}
