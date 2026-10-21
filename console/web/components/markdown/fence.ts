/* What a fence's opening line says about its block.

   The language tag can carry a variant after a colon — ```shell:openai — which
   <Variants> reads to label its tabs, and the words after the tag are the
   fence's meta, where a block names the file it shows:

       ```ts title="src/cantelop.ts"

   Parsing lives here, apart from the rendering, so the rules are one small
   pure module rather than a corner of the <pre> override. */
export type Fence = {
  language: string;
  variant?: string;
  title?: string;
  /* A terminal transcript: prompts dimmed, "→ " answers shown but never copied. */
  shell: boolean;
  /* A file, read by line, so the lines are numbered. */
  numbered: boolean;
};

const SHELL_LANGUAGES = new Set(["shell", "console", "bash", "sh"]);

/* The name a block wears when its fence gives it no file. A transcript is a
   terminal; a file is named after its language; plain text has nothing to say
   about itself. */
const KINDS: Record<string, string> = {
  ts: "TypeScript",
  typescript: "TypeScript",
  tsx: "TypeScript",
  json: "JSON",
  dockerfile: "Dockerfile",
};

export function parseFence(className = "", meta = ""): Fence {
  const tag = /language-(\S+)/.exec(className)?.[1] ?? "";
  const [language, variant] = tag.split(":");
  const shell = SHELL_LANGUAGES.has(language);
  const title = attribute(meta, "title");
  return {
    language,
    variant,
    title,
    shell,
    numbered: !shell && language !== "" && language !== "text",
  };
}

export function kindOf({ language, shell }: Fence): string {
  return shell ? "Terminal" : (KINDS[language] ?? "");
}

/* name="value", name='value', or name=value, anywhere in the meta. */
function attribute(meta: string, name: string): string | undefined {
  const match = new RegExp(`(?:^|\\s)${name}=(?:"([^"]*)"|'([^']*)'|(\\S+))`).exec(meta);
  return match?.slice(1).find((value) => value !== undefined);
}
