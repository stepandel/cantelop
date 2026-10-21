# Cantelop hello App

This stable SDK 1.0 example defines one named App, a Session runtime that echoes
messages through output and request replies, and an optional homepage. The SDK
generates the Edge command endpoints. SDK 1.0 is the only supported SDK.

Use Node.js 22+, Cantelop CLI 0.15.2+, and Bun 1.3.10 for native development.
Deployment and container development require Docker with Linux amd64 support.
From this directory:

```sh
npm ci
cantelop dev
```

The CLI builds `.cantelop/client/client.mjs` and records this App's local
integration credentials in a private SDK profile. In a second terminal, run:

```sh
CANTELOP_PROFILE=local node --input-type=module --eval '
  import { hello } from "./.cantelop/client/client.mjs";
  const session = hello.workspace({slug:"demo"}).session({id:"echo"});
  console.log(await session.request({message:"hello"}));
  await session.stop();
'
```

Visit `http://127.0.0.1:8787/` for the homepage. Workspace files remain under
`.cantelop/dev/hello/workspaces/`. The App definition's name selects deployment
identity; `cantelop.json` selects the definition file.

```sh
cantelop login
cantelop deploy --create-app
```

Choose an available App name in `src/cantelop.ts` before deploying. Deployment
builds a matching Edge/runtime release and writes production integration
credentials. Use `CANTELOP_PROFILE=default` when calling the deployed App from
the same machine. Rebuild and deploy your backend from the same source; see
[deploy your backend with the App](https://cantelop.com/docs/deploy#deploy-your-backend-with-the-app).

In a customer backend, authenticate users before selecting their Workspace.
Integration credentials belong on the backend. For a backend hosted elsewhere,
add the output of `cantelop app credentials --app hello` to that host's secrets;
`cantelop app credentials rotate` replaces the token with a grace period.

`session.stream()` reads output; resume explicitly with
`session.stream({after: savedCursor})`. Persist application state in Workspace
files or its database. SDK 1.0's durable view APIs are not yet available on
hosted Cantelop, so this example uses output and replies without view commits.

This simple `receive` runtime handles one echo at a time. For long-running agent
work, use a harness factory with `queue`, `steer`, and `cancel`, or supervised
activity. `session.cancel()` selects the active harness operation and preserves
queued input; it takes no target message ID. `session.stop()` releases the
Sandbox separately. See the [Session runtime guide](https://cantelop.com/docs/session-runtime).
