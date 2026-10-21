import type { AppTraceTree } from "@core/observability/trace-tree";

/** Keep a linked request visible when it falls outside the app's recent roots. */
export function includeLinkedRequest(tree: AppTraceTree, linked: AppTraceTree): AppTraceTree {
  const request = linked.requests[0];
  if (!request) return tree;
  return {
    ...tree,
    requests: [request, ...tree.requests.filter(({ id }) => id !== request.id)],
  };
}
