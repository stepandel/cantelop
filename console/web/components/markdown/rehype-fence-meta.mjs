/* Carries a fence's meta — the words after its language, as in
   ```ts title="src/cantelop.ts" — onto the <code> element as a `meta` prop, where
   the <pre> override in components.tsx reads it. remark-rehype keeps the
   meta in the node's data, which never becomes a prop on its own.

   next.config.ts loads this by path: Turbopack takes MDX plugins as strings
   to resolve, not as functions, and the loader imports the file itself. It
   stays plain JavaScript for that reason. */
export default function rehypeFenceMeta() {
  return (tree) => visit(tree);
}

function visit(node) {
  if (node.type === "element" && node.tagName === "code" && node.data?.meta) {
    node.properties.meta = node.data.meta;
  }
  for (const child of node.children ?? []) {
    visit(child);
  }
}
