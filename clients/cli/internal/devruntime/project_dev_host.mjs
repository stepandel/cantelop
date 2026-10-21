import { readFile, stat } from "node:fs/promises";
import { createServer } from "node:http";
import { Readable } from "node:stream";
import { pathToFileURL } from "node:url";

const [workerPath, portDocument, bindingsPath, bridgeOrigin] = process.argv.slice(2);
const port = Number(portDocument);
if (!workerPath || !bindingsPath || !bridgeOrigin || !Number.isInteger(port) || port < 1 || port > 65535) {
  throw new Error("invalid Cantelop local API host arguments");
}
const bridgeURL = new URL(bridgeOrigin);
if (bridgeURL.protocol !== "http:" || bridgeURL.hostname !== "127.0.0.1" || bridgeURL.pathname !== "/" || bridgeURL.search || bridgeURL.hash) {
  throw new Error("invalid Cantelop local Session bridge origin");
}
const credentialChunks = [];
for await (const chunk of process.stdin) credentialChunks.push(chunk);
const bridgeToken = Buffer.concat(credentialChunks).toString("utf8");
if (!/^[0-9a-f]{64}$/.test(bridgeToken)) throw new Error("invalid Cantelop local Session bridge credential");
const nativeFetch = globalThis.fetch;
globalThis.fetch = async (input, init) => {
  const target = new URL(input instanceof Request ? input.url : input);
  if (target.origin !== bridgeURL.origin) return nativeFetch(input, init);
  const request = new Request(input, init);
  request.headers.set("X-Cantelop-Dev-Bridge-Token", bridgeToken);
  return nativeFetch(new Request(request, { redirect: "manual" }));
};
const bindings = Object.freeze(JSON.parse(await readFile(bindingsPath, "utf8")));
const workerURL = pathToFileURL(workerPath);
let worker;
let workerVersion = "";
async function loadWorker() {
  const source = await stat(workerPath);
  const version = `${source.mtimeMs}-${source.size}`;
  if (worker && version === workerVersion) return worker;
  const loaded = (await import(`${workerURL.href}?version=${encodeURIComponent(version)}`)).default;
  if (!loaded || typeof loaded.fetch !== "function") {
    throw new Error("local API artifact does not export a Worker fetch handler");
  }
  worker = loaded;
  workerVersion = version;
  return worker;
}
await loadWorker();

const portSuffix = port === 80 ? "" : `:${port}`;
const localHosts = new Set(["127.0.0.1", "localhost"].map((name) => `${name}${portSuffix}`));
const localOrigins = new Set([...localHosts].map((host) => `http://${host}`));
function allowedRequest(incoming) {
  const values = (name) => incoming.rawHeaders.flatMap((value, index, headers) =>
    index % 2 === 0 && value.toLowerCase() === name ? [headers[index + 1]] : []);
  const hosts = values("host");
  const origins = values("origin");
  const sites = values("sec-fetch-site");
  if (hosts.length !== 1 || !localHosts.has(hosts[0].toLowerCase()) || origins.length > 1 || sites.length > 1) return false;
  if (origins.length === 1 && !localOrigins.has(origins[0])) return false;
  if (sites.length === 1 && sites[0] !== "same-origin" && sites[0] !== "none") return false;
  return true;
}

const server = createServer(async (incoming, outgoing) => {
  try {
    if (!allowedRequest(incoming)) {
      outgoing.writeHead(403, { "Content-Type": "application/json", "Cache-Control": "no-store" });
      outgoing.end(JSON.stringify({ error: { code: "invalid_origin" } }));
      return;
    }
    const method = incoming.method ?? "GET";
    const request = new Request(`http://127.0.0.1:${port}${incoming.url ?? "/"}`, {
      method,
      headers: incoming.headers,
      redirect: "manual",
      ...(method === "GET" || method === "HEAD" ? {} : {
        body: Readable.toWeb(incoming),
        duplex: "half",
      }),
    });
    const response = await (await loadWorker()).fetch(request, bindings);
    outgoing.writeHead(response.status, Object.fromEntries(response.headers));
    if (response.body === null) {
      outgoing.end();
      return;
    }
    Readable.fromWeb(response.body).pipe(outgoing);
  } catch (error) {
    console.error(error);
    if (!outgoing.headersSent) {
      outgoing.writeHead(500, { "Content-Type": "application/json", "Cache-Control": "no-store" });
    }
    outgoing.end(JSON.stringify({ error: { code: "api_unavailable" } }));
  }
});

server.listen(port, "127.0.0.1", () => {
  process.stdout.write(`CANTELOP_LOCAL_READY ${port}\n`);
});

const shutdown = () => server.close(() => process.exit(0));
process.once("SIGINT", shutdown);
process.once("SIGTERM", shutdown);
