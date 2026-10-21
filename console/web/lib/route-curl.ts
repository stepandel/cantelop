const BODY_METHODS = new Set(["PATCH", "POST", "PUT"]);

/** A curl command to start from. The platform knows a route's method and
 *  path, not its body or the authorization the App expects. */
export function curlCommand(origin: string, route: { method: string; path: string }): string {
  const url = `'${`${origin}${route.path}`.replaceAll("'", "'\\''")}'`;
  if (route.method === "GET") return `curl ${url}`;
  if (route.method === "HEAD") return `curl -I ${url}`;
  return BODY_METHODS.has(route.method)
    ? `curl -X ${route.method} ${url} -H 'content-type: application/json' -d '{}'`
    : `curl -X ${route.method} ${url}`;
}
