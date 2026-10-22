import assert from 'node:assert/strict';
import { createRequire } from 'node:module';

// Use the existing Edge development toolchain; no additional installation.
const requireFromEdge = createRequire(new URL('../../edge/workers/package.json', import.meta.url));
const { Miniflare, convertV4MiniflareOptions } = requireFromEdge('miniflare');

// Disposable runtime experiment, not the production routing/authentication path.
const appCode = `
import { WorkerEntrypoint } from 'cloudflare:workers';
let firstSecret;
export default class App extends WorkerEntrypoint {
  async fetch(request) {
    firstSecret ??= this.env.SECRET;
    await new Promise(resolve => setTimeout(resolve, Math.floor(Math.random() * 8)));
    const runtime = await this.ctx.props.runtime.fetch(request);
    let unrestrictedEgress = true;
    try { await fetch('https://forbidden.invalid/'); }
    catch { unrestrictedEgress = false; }
    return Response.json({
      app: this.env.APP, secret: this.env.SECRET, firstSecret,
      package: this.env.PACKAGE, revision: this.env.REVISION,
      context: await runtime.json(), unrestrictedEgress,
    });
  }
}`;
const loaderCode = `
import { WorkerEntrypoint } from 'cloudflare:workers';
const appCode = ${JSON.stringify(appCode)};
export class Runtime extends WorkerEntrypoint {
  async fetch(request) {
    // Deliberately ignore caller-supplied app and request headers.
    return Response.json(this.ctx.props);
  }
}
export default {
  async fetch(request, env, ctx) {
    const url = new URL(request.url);
    const app = url.searchParams.get('app');
    const revision = url.searchParams.get('revision') || '1';
    const pkg = url.searchParams.get('package') || 'p1';
    const requestId = url.searchParams.get('request');
    const key = JSON.stringify(['runner-v1', app, pkg, revision]);
    const worker = env.LOADER.get(key, () => ({
      compatibilityDate: '2026-08-01',
      mainModule: 'app.js', modules: { 'app.js': appCode },
      env: { APP: app, SECRET: app + ':secret:' + revision, PACKAGE: pkg, REVISION: revision },
      globalOutbound: null,
    }));
    // This capability belongs to the invocation, not the cached WorkerCode.
    return worker.getEntrypoint(undefined, { props: {
      runtime: ctx.exports.Runtime({ props: { app, requestId } }),
    }}).fetch(request);
  }
}`;

const mf = new Miniflare(convertV4MiniflareOptions({
  cf: false,
  workers: [{
    name: 'fleet-sharing-probe',
    modules: true,
    script: loaderCode,
    compatibilityDate: '2026-08-01',
    workerLoaders: { LOADER: {} },
  }],
}));
async function invoke(app, requestId, revision = '1', pkg = 'p1') {
  const query = new URLSearchParams({ app, request: requestId, revision, package: pkg });
  const response = await mf.dispatchFetch('https://probe.invalid/?' + query, {
    headers: { 'x-app-id': 'forged-app', 'x-request-id': 'forged-request' },
  });
  assert.equal(response.status, 200, await response.clone().text());
  return response.json();
}
try {
  const results = await Promise.all(Array.from({ length: 40 }, async (_, index) => {
    const app = index % 2 ? 'alice' : 'bob';
    const requestId = 'request-' + index;
    const value = await invoke(app, requestId);
    assert.equal(value.app, app);
    assert.equal(value.secret, app + ':secret:1');
    assert.equal(value.firstSecret, value.secret);
    assert.deepEqual(value.context, { app, requestId });
    assert.equal(value.unrestrictedEgress, false);
    return value;
  }));
  assert.equal(new Set(results.map(value => value.context.requestId)).size, 40);
  const rotated = await invoke('alice', 'rotation', '2');
  assert.equal(rotated.secret, 'alice:secret:2');
  assert.equal(rotated.firstSecret, rotated.secret);
  const upgraded = await invoke('alice', 'upgrade', '2', 'p2');
  assert.equal(upgraded.package, 'p2');
  const rollback = await invoke('alice', 'rollback', '2', 'p1');
  assert.equal(rollback.package, 'p1');
  assert.equal(rollback.secret, 'alice:secret:2');
  assert.equal(rollback.context.requestId, 'rollback');
  console.log('PASS: 40 overlapping invocations; isolated globals and secrets; request-scoped runtime capabilities; forged headers ignored; unrestricted egress blocked; configuration rotation; package upgrade and rollback with current configuration.');
} finally {
  await mf.dispose();
}
