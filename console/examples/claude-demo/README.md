# Website Claude demo

This App uses the published `@cantelop/sdk@1.0.0` directly. The SDK owns
the Claude runner, native link/code login, durable queue, streaming, and recovery.
There is no dependency on the separate `claude-code-api` project and no copied
Claude implementation. `cantelop.ts` declares the demo's file-only tools,
eight-turn limit, and native auth provider.

From `console/`:

```sh
bun install --frozen-lockfile
bun run claude-demo:build
bun test web/app/api/examples/claude-plan/demo-routes.test.mjs
bun run typecheck
```

The build compiles the Edge and Sandbox artifacts into `.cantelop/claude-demo/`
and copies the credential-free backend client into the website's
`web/app/api/examples/claude-plan/generated/` directory. Commit the generated
client whenever the App definition or SDK changes. Website builds consume the
checked-in client and do not require Docker or Cantelop credentials.

## Deployment

Use CLI 0.15.2+ and Docker with Linux amd64 support. From `console/`, the complete
image build is `cantelop build -config cantelop.claude-demo.json`; deployment is
`cantelop deploy -config cantelop.claude-demo.json`.

The App name stays `cantelop-claude-api`. Keep the existing visitor-cookie signing
secret (`CLAUDE_DEMO_PRIVATE_JWK`) and Workspace mapping: changing either would
disconnect returning visitors from their saved native sign-ins and files.
The backend requires the App-scoped integration profile in Vercel's secret
production and preview `CANTELOP_APP_CONFIG` environment.

The generated backend, Edge, and Sandbox must carry the same runtime ID; see
[deploy your backend with the App](https://cantelop.com/docs/deploy#deploy-your-backend-with-the-app). Configure the integration
profile before activating the matching website and App releases. Retain matching
SDK 1.0 website/App releases for rollback.

Qualify preview login, rejected-code retry, cancellation, returning-visitor login,
all three model choices, follow-up conversation, streamed text/tool status, and
two visitors with isolated Workspaces before the production cutover. Pending
login attempts from the previous App protocol expire; users can reconnect to
start a new attempt. Native credentials and Workspace files remain reusable.
