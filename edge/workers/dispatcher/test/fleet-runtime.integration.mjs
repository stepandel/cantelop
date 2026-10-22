import assert from "node:assert/strict";
import test from "node:test";
import { createHash } from "node:crypto";
import { fileURLToPath } from "node:url";
import { build } from "esbuild";
import { Miniflare, convertV4MiniflareOptions } from "miniflare";

const enabled = process.env.CANTELOP_TEST_WORKER_RUNTIME === "1";
test("shared package uses isolated contexts, fresh capabilities and fenced configuration in workerd", { skip: !enabled, timeout: 30_000 }, async () => {
  const bundle = await build({ entryPoints: [fileURLToPath(new URL("../src/entrypoint.ts", import.meta.url))],
    bundle: true, write: false, format: "esm", platform: "browser", external: ["cloudflare:workers"] });
  const code = `
    let firstSecret;
    export default { async fetch(request, env, ctx) {
      if (new URL(request.url).pathname === '/terminal') {
        const pair = new WebSocketPair();
        pair[1].accept(); pair[1].send('terminal-ready');
        pair[1].addEventListener('close', () => pair[1].close());
        return new Response(null, { status: 101, webSocket: pair[0] });
      }
      if (new URL(request.url).pathname === '/native-terminal') {
        return ctx.props.CANTELOP_RUNTIME.fetch(new Request('https://runtime.cantelop.internal/__cantelop/integration/v2/sessions/cantelop-native-auth-wsp_' + '1'.repeat(32) + '/auth/terminals/trm_' + '2'.repeat(32) + '?workspace_id=wsp_' + '1'.repeat(32), { headers: { Upgrade: 'websocket' } }));
      }
      if (new URL(request.url).pathname === '/stream') {
        return new Response(new ReadableStream({ start(controller) {
          controller.enqueue(new TextEncoder().encode('streamed')); controller.close();
        } }));
      }
      firstSecret ??= env.SECRET;
      const target = new Request('https://runtime.cantelop.internal/__cantelop/v1/workspaces/open', { method: 'POST', body: '{}' });
      const reply = await ctx.props.CANTELOP_RUNTIME.fetch(target.clone());
      const bypass = await fetch(target);
      const restricted = await fetch('https://metadata.internal/');
      return Response.json({ secret: env.SECRET, firstSecret, scope: await reply.json(), bypass: bypass.status, restricted: restricted.status });
    } };`;
  const pkg = `pkg_${"1".repeat(32)}`, secondPackage = `pkg_${"2".repeat(32)}`;
  const token = "a".repeat(64);
  const appA = `app_${"a".repeat(32)}`, appB = `app_${"b".repeat(32)}`;
  const generation = `act_${"0".repeat(31)}1`;
  const observations = [];
  const mf = new Miniflare(convertV4MiniflareOptions({ cf: false, workers: [{
    name: "cantelop-dispatcher", modules: true, script: bundle.outputFiles[0].text,
    compatibilityDate: "2026-08-11", workerLoaders: { FLEET_LOADER: {} },
    durableObjects: { RELEASE_GATES: { className: "ReleaseGate", useSQLite: true }, FLEET_PACKAGES: { className: "FleetPackage", useSQLite: true } },
    kvNamespaces: ["APP_ROUTES"],
    bindings: { RELEASE_GATE_TOKEN: token, OBSERVABILITY_INGEST_TOKEN: token, OBSERVABILITY_INGEST_ORIGIN: "https://observations.example" },
    serviceBindings: { BROKER: "test-broker" },
    outboundService: async request => { observations.push(await request.json()); return new Response(null, { status: 204 }); },
  }, {
    name: "test-broker", modules: true, compatibilityDate: "2026-08-11",
    script: `import { WorkerEntrypoint } from 'cloudflare:workers';
      export default class extends WorkerEntrypoint { dispatchManaged(_request, scope) { return Response.json(scope); } }`,
  }] }));
  const control = async (app, body) => mf.dispatchFetch(`https://release-gate.cantelop.dev/__cantelop/release-gates/${app}`, {
    method: "PUT", headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" }, body: JSON.stringify(body),
  });
  const select = async (app, id, secret, revision, extra = {}) => {
    assert.equal((await control(app, { action: "close", generation })).status, 200);
    assert.equal((await control(app, { action: "configure", revision, bindings: { ...extra, SECRET: secret } })).status, 200);
    assert.equal((await control(app, { action: "select", generation, script_name: id })).status, 200);
    assert.equal((await control(app, { action: "open", generation })).status, 200);
  };
  try {
    const digest = `sha256:${createHash("sha256").update(code).digest("hex")}`;
    const publish = (id, module = code) => mf.dispatchFetch(`https://release-gate.cantelop.dev/__cantelop/packages/${id}`, {
      method: "PUT", headers: { Authorization: `Bearer ${token}` },
      body: JSON.stringify({ module, digest, compatibility_date: "2026-08-11" }),
    });
    assert.equal((await publish(pkg)).status, 200);
    assert.equal((await publish(pkg)).status, 200);
    assert.equal((await publish(secondPackage)).status, 200);
    assert.equal((await publish(pkg, code + "// changed")).status, 400);
    const routes = await mf.getKVNamespace("APP_ROUTES");
    await routes.put("alice", appA);
    await routes.put("bob", appB);
    const largeConfiguration = Object.fromEntries(Array.from({ length: 128 }, (_, i) => [`VALUE_${i}`, "x".repeat(2048)]));
    await select(appA, pkg, "alice-v1", 1, largeConfiguration);
    await select(appB, pkg, "bob-v1", 1);
    const directGates = await mf.getDurableObjectNamespace("RELEASE_GATES");
    const direct = await directGates.get(directGates.idFromName(appA)).fetch("https://alice.cantelop.dev/", { headers: {
      "x-cantelop-release-gate-app": appA,
      "x-cantelop-release-gate-invocation": JSON.stringify({ app_id: appA, request_id: `req_${"c".repeat(32)}`, hostname: "alice.cantelop.dev" }),
    } });
    assert.equal(direct.status, 200);
    await direct.body?.cancel();
    const invoke = slug => mf.dispatchFetch(`https://${slug}.cantelop.dev/`, {
      headers: { "X-Cantelop-Edge-App-ID": appB, "X-Cantelop-Edge-Request-ID": "req_forged" },
    });
    const replies = await Promise.all(Array.from({ length: 24 }, async (_, i) => {
      const slug = i % 2 ? "alice" : "bob", app = i % 2 ? appA : appB;
      const response = await invoke(slug);
      assert.equal(response.status, 200);
      const value = await response.json();
      assert.equal(value.secret, `${slug}-v1`);
      assert.equal(value.firstSecret, value.secret);
      assert.equal(value.scope.app_id, app);
      assert.match(value.scope.request_id, /^req_[0-9a-f]{32}$/);
      assert.equal(value.bypass, 403);
      assert.equal(value.restricted, 403);
      return value;
    }));
    assert.equal(new Set(replies.map(value => value.scope.request_id)).size, 24);
    const streamed = await mf.dispatchFetch('https://alice.cantelop.dev/stream');
    assert.equal(await streamed.text(), 'streamed');
    const unsupported = await mf.dispatchFetch('https://alice.cantelop.dev/native-terminal', { headers: { Upgrade: 'websocket' } });
    assert.equal(unsupported.status, 404);
    const terminal = await mf.dispatchFetch('https://alice.cantelop.dev/terminal', { headers: { Upgrade: 'websocket' } });
    assert.equal(terminal.status, 101, terminal.status === 101 ? "" : await terminal.text());
    assert.ok(terminal.webSocket);
    const ready = new Promise(resolve => terminal.webSocket.addEventListener('message', event => resolve(event.data), { once: true }));
    terminal.webSocket.accept();
    assert.equal(await ready, 'terminal-ready');
    const socketClosed = new Promise(resolve => terminal.webSocket.addEventListener('close', resolve, { once: true }));
    assert.equal((await control(appA, { action: "configure", revision: 2, bindings: { SECRET: "alice-v2" } })).status, 200);
    assert.equal((await socketClosed).code, 1012);
    assert.equal((await control(appA, { action: "configure", revision: 1, bindings: { SECRET: "alice-v1" } })).status, 409);
    assert.equal((await control(appA, { action: "configure", revision: 2, bindings: { SECRET: "different" } })).status, 409);
    const rotated = await (await invoke("alice")).json();
    assert.equal(rotated.secret, "alice-v2");
    assert.equal(rotated.firstSecret, "alice-v2");
    const gate = (await mf.getDurableObjectNamespace("RELEASE_GATES"));
    const stale = await gate.get(gate.idFromName(appA)).fetch("https://release-gate.internal/authorize", {
      method: "POST", body: JSON.stringify({ app_id: appA, package_id: pkg, generation, configuration_revision: 1 }),
    });
    assert.equal(stale.status, 403);
    await select(appA, secondPackage, "alice-v2", 2);
    await select(appA, pkg, "alice-v2", 2);
    assert.equal((await (await invoke("alice")).json()).secret, "alice-v2");
    assert.equal((await control(appA, { action: "close", generation })).status, 200);
    assert.equal((await control(appA, { action: "purge", generation })).status, 200);
    assert.equal((await control(appA, { action: "configure", revision: 3, bindings: { SECRET: "resurrected" } })).status, 409);
    assert.equal((await control(appA, { action: "open", generation })).status, 409);
    assert.equal((await invoke("alice")).status, 503);
    assert.equal((await invoke("bob")).status, 200);
    const readback = await mf.dispatchFetch(`https://release-gate.cantelop.dev/__cantelop/release-gates/${appB}`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    assert.equal((await readback.text()).includes("bob-v1"), false);
  } finally { await mf.dispose(); }
});
