/** The route a detail request names: ?method=POST&path=/chat. */
export function routeQuery(searchParams: URLSearchParams): { method: string; path: string } {
  const method = searchParams.get("method") ?? "";
  const path = searchParams.get("path") ?? "";
  if (!/^[A-Z]{1,32}$/.test(method) || !path.startsWith("/") || path.length > 2048 || /[\x00-\x1f\x7f]/.test(path)) {
    throw new TypeError("invalid route");
  }
  return { method, path };
}
