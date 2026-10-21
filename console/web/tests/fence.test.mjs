import { expect, test } from "bun:test";
import { kindOf, parseFence } from "../components/markdown/fence.ts";
import rehypeFenceMeta from "../components/markdown/rehype-fence-meta.mjs";

test("a fence's language, variant, and title are read apart", () => {
  expect(parseFence("language-ts", 'title="src/cantelop.ts"')).toEqual({
    language: "ts",
    variant: undefined,
    title: "src/cantelop.ts",
    shell: false,
    numbered: true,
  });
  expect(parseFence("language-text:openai", "title='.env'")).toMatchObject({
    language: "text",
    variant: "openai",
    title: ".env",
    numbered: false,
  });
  expect(parseFence("language-shell", "title=Dockerfile")).toMatchObject({ shell: true, numbered: false, title: "Dockerfile" });
});

test("a bare fence is plain text with nothing to say about itself", () => {
  const fence = parseFence(undefined, undefined);
  expect(fence).toEqual({ language: "", variant: undefined, title: undefined, shell: false, numbered: false });
  expect(kindOf(fence)).toBe("");
});

test("a block without a file is named for what it is", () => {
  expect(kindOf(parseFence("language-shell"))).toBe("Terminal");
  expect(kindOf(parseFence("language-tsx"))).toBe("TypeScript");
  expect(kindOf(parseFence("language-json"))).toBe("JSON");
  expect(kindOf(parseFence("language-dockerfile"))).toBe("Dockerfile");
  expect(kindOf(parseFence("language-text"))).toBe("");
});

test("the rehype plugin carries a fence's meta onto the code element", () => {
  const code = { type: "element", tagName: "code", properties: { className: ["language-ts"] }, data: { meta: 'title="a.ts"' }, children: [] };
  const bare = { type: "element", tagName: "code", properties: {}, children: [] };
  const tree = { type: "root", children: [{ type: "element", tagName: "pre", properties: {}, children: [code] }, bare] };
  rehypeFenceMeta()(tree);
  expect(code.properties.meta).toBe('title="a.ts"');
  expect(bare.properties.meta).toBeUndefined();
});
