# Cantelop customer CLI

The `cantelop` CLI creates Apps and deploys complete releases of an App's
generated Edge Worker and Session runtime without sending large artifacts
through the Next.js console. Every deployment is bound to an existing App, and
the control plane activates it only after both release components succeed.

SDK **1.0** is the only supported SDK. Use Cantelop CLI **0.15.2 or newer**,
Node.js **22 or newer**, and `npm install @cantelop/sdk`. Earlier SDKs and
1.0 prereleases are unsupported.

Run `cantelop --help` (or `cantelop -h`) for a grouped command overview and
common examples. `cantelop help` is also available as a shell-friendly alias.

## Install and log in

Install the current qualified production release with Homebrew:

```sh
brew install stepandel/tap/cantelop
```

The curl installer remains available as an alternative on macOS or Linux:

```sh
curl -fsSL https://cantelop.com/install.sh | sh
```

It detects amd64 or arm64, downloads the matching archive from the public
`stepandel/homebrew-tap` release, verifies its embedded SHA-256 checksum, checks
the CLI's reported version, and atomically installs it to
`${CANTELOP_INSTALL_DIR:-$HOME/.local/bin}`. When the default directory is not
on PATH, it adds an idempotent entry to the current shell's profile and prints
the command needed to activate it. To leave shell profiles untouched:

```sh
curl -fsSL https://cantelop.com/install.sh \
  | CANTELOP_NO_MODIFY_PATH=1 sh
```

Set a custom destination on the shell that runs the installer, not on `curl`:

```sh
curl -fsSL https://cantelop.com/install.sh \
  | CANTELOP_INSTALL_DIR="$HOME/bin" sh
```

Custom destinations are not added to PATH automatically. The curl installer
refuses to create a second copy when `cantelop` is already available from
Homebrew or another location.

For automation that must pin an exact release, download that release's
versioned installer asset instead of the moving console URL:

```sh
version=0.15.2
curl -fsSL "https://github.com/stepandel/homebrew-tap/releases/download/cantelop-v${version}/install.sh" | sh
```

For development, build the CLI with the Go release declared by
[`go.mod`](../go.mod), or a compatible newer release:

```sh
go build -o cantelop ./clients/cli/cmd/cantelop
```

Pre-production release candidates are produced independently from host
artifacts:

```sh
make check-cli-release
make cli-artifacts
```

`cli-artifacts` writes checksum-covered macOS and Linux archives for amd64 and
arm64 under `dist/cli`. Each archive contains `cantelop` and this guide. Run
`cantelop version` to verify the embedded semantic version and source commit.
Code signing, notarization, GitHub release upload, and Homebrew/curl publication
are production distribution operations and do not occur during these builds.
The production operator follows the
[macOS CLI signing and notarization runbook](./macos-cli-release.md) without
modifying or rebuilding the selected qualified artifacts.

Node.js, a project-installed `@cantelop/sdk`, and Docker are required for the
one-command build and deployment flow. Log in against the production console:

```sh
cantelop login
```

The command starts a temporary callback on numeric IPv4 loopback and prints a
sign-in URL on `https://cantelop.com`. Open it in a browser, sign in with GitHub or Google as usual,
and choose **Authorize CLI**. Provider credentials remain inside the browser and
console session. The CLI receives only a signed Cantelop refresh credential and
stores it in the operating system's user configuration directory with mode
`0600`. Set `CANTELOP_CONFIG` to use an explicit credential-file path.

For a non-production installation, select its console origin explicitly:

```sh
cantelop login -console https://console.example.com
```

Plain HTTP is accepted only for local development origins.

## Diagnose a project

Run the read-only project diagnostic before local development or deployment:

```sh
cantelop doctor
```

Use `-config PATH` for a non-default manifest and `--app NAME` when the App
definition exports more than one App. The command checks Node.js and Bun, the
Docker CLI and daemon, and `linux/amd64` builder support. It then compiles the
App definition named in `cantelop.json` to read the App's name, `environment`
declarations, and optional `dockerfile`, and checks the project-installed SDK
against the CLI. With live authentication, it resolves the App name to its App
ID and confirms that every `required` variable and secret declared in
`environment` exists with the declared kind. Every independent check runs even
when an earlier one fails. Results are printed as `[pass]`, `[fail]`, `[warn]`,
or `[skip]`, and the command exits unsuccessfully when any check fails; warnings
do not fail it.

SDK 1.0 uses CLI build protocol **6**, actor protocol **2**, integration
protocol **2**, and local database protocol **1**. `cantelop doctor` reads the
project-installed SDK's version and protocols and fails when the CLI does not
support them. Install SDK 1.0 with `npm install @cantelop/sdk`; upgrade the CLI
when the SDK's build protocol is newer than the CLI supports.
The accepted SDK must report build protocol version `4` or `5` or `6`;
SDK 1.0 reports `6`.

The SDK generates the Edge routes from the App definition. Builds and deploys
print one `/commands/{type}` path per command, plus declared webhooks and an
optional homepage. The console's Ingress page shows those routes beside observed
traffic. The App has no customer-authored command router.

## Create an App

List or inspect existing Apps, and archive an App that is no longer needed:

```sh
cantelop app list
cantelop app inspect APP_ID
cantelop app archive APP_ID
```

Archival immediately removes the App and its Workspaces from product reads and
queues provider cleanup, which then permanently purges every Workspace's files
and managed database. It does not delete local project files.

Create a provisioning App explicitly when you need its generated ID before the
first deployment:

```sh
cantelop app create -slug hello
```

The App is publicly available at `https://hello.cantelop.dev`. Workspaces are
materialized by the SDK when application code calls
`app.workspace({ slug }).resolve()`, or dispatches through
`app.workspace({ slug }).session({ id })`. Use one Workspace per user or account
with the slug as the isolation boundary; App creation does not create one.
Constructing these lazy references alone does not provision resources.
Creation uses the platform's default 4-vCPU Sandbox profile with 512 MiB boot
memory and up to 4096 MiB of automatic hotplug growth, one on-demand
request-owned, workload-completed Session, and zero prewarmed Sessions.
Cantelop assigns the private Session runtime listener port and passes it to the SDK;
it is not application configuration.
Sandbox idle lifetime is not an App setting. Callers choose it explicitly with
each SDK Session's `keepAliveSeconds`. The command prints the generated App ID
for API and operator commands; project source names the App only by its slug,
the `name` passed to `cantelop.app(...)`. Apps created in the web console follow
the same deployment path.

## Initialize a project

Generate an agent project after choosing the target App slug and the agent SDK
its Session runtime uses:

```sh
cantelop init -app hello -provider openai
cantelop init -app hello -provider claude
cantelop init -app hello -provider pi
```

The command writes four files without requiring login:

- `cantelop.json`, the project manifest, which names only the App definition.
- `src/cantelop.ts`, the App definition: the App's name, runtime, and
  environment declarations in one `cantelop.app(...)` call.
- `src/agent.ts`, the Session runtime: an exported `receive` handler that
  passes each Message's `prompt` to the chosen agent SDK inside the Sandbox.
- `package.json`, which depends on that agent SDK.

`-app` is the App name the project deploys to; the App need not exist yet.
`-provider` is required and selects `@openai/codex-sdk` (`openai`),
`@anthropic-ai/claude-agent-sdk` (`claude`), or
`@earendil-works/pi-coding-agent` (`pi`). Use `-config` to write the manifest
somewhere other than `cantelop.json`, and `-definition` to place the App
definition at another path relative to the manifest. The Session runtime is
always `agent.ts` beside the definition, so the definition cannot itself be
named `agent.ts`. Creation is atomic and refuses to overwrite any existing
target.

Run `npm install @cantelop/sdk` followed by `cantelop dev` after generation.
The generated `package.json` names the agent SDK but not `@cantelop/sdk`: that
first install adds the stable SDK 1.x range, so the CLI never pins a project to
the SDK that was current when the CLI was built.

The manifest only points the CLI at the definition module:

```json
{
  "schema_version": 3,
  "definition": "src/cantelop.ts"
}
```

Everything else about the App is declared in code. For the Claude starter:

```ts
import { CantelopClient } from "@cantelop/sdk";
import { receive } from "./agent.js";

const cantelop = new CantelopClient();
export const agent = cantelop.app<{ prompt: string }>({
  name: "hello",
  runtime: { receive },
  environment: {
    ANTHROPIC_API_KEY: { secret: true, required: true },
  },
});
```

Starters generated by CLI 0.15.2 omit the inline message type
`cantelop.app<{ prompt: string }>`; add it before running `cantelop dev` to
avoid a declaration-generation error. Later CLI releases generate it.

The OpenAI starter declares `CODEX_API_KEY` the same way. The Pi starter
declares no environment: Pi discovers any provider credential supported by its
model runtime, and Sessions receive only the values the App declares, so add
the credential your provider needs to `environment`, for example
`ANTHROPIC_API_KEY: { secret: true, required: true }`. Set local values in
`.env` and keep it out of version control.

The starter contains no HTTP API. Cantelop generates the App's Edge Worker,
which accepts commands at `/commands/{command}`, one path per SDK call. A
backend reaches the App through the same `agent` export: `cantelop dev` writes
a compiled backend client to `.cantelop/client/` that keeps the App's exports
but none of its Sandbox code. The App's integration credential authorizes every
Workspace, so it stays on that backend; authenticate your own users there and
choose their Workspace before opening a Session. Optional App webhooks verify
the provider's signature in their own handler.

## Develop an App locally

Run the App locally without logging in or starting fire-fuse:

```sh
cantelop dev
```

The command reads `cantelop.json`, compiles the selected App into a generated local Edge Worker
and Session runtime, and serves the App on `http://127.0.0.1:8787`. By default,
each Sandbox is a native Bun 1.3.10 subprocess, so startup does not build or start a
Docker container. The same compilation writes the backend client to
`.cantelop/client/`, and the CLI records a local integration credential in the
SDK's `local` profile, so a backend run with `CANTELOP_PROFILE=local` reaches the
local App. Use another port or configuration file when needed, and `--app NAME`
when the definition exports more than one App:

```sh
cantelop dev -port 3000 -config path/to/cantelop.json
```

Non-secret defaults declared in the App's `environment` supply the initial local App
environment. The optional project-root `.env` file overrides those defaults
for both the Edge Worker and the Session runtime. A missing default file is allowed; selecting a
file explicitly makes it required:

```sh
cantelop dev -env-file .env.development
```

Names beginning with `CANTELOP_` are reserved and rejected. Values are never
placed in process arguments; the CLI writes private temporary binding and
environment files that are removed on shutdown.

Local development uses a loopback-only, in-memory implementation of the
Workspace and Session bridge. In the default mode, each active Session owns one
private Bun subprocess running the normal bundled Session runtime protocol. The CLI
watches the App definition's import graph, plus `db/schema.ts` when present.
Each change recompiles the Edge Worker, Session runtime, and backend client
together and applies schema changes to the local Workspace databases. The local
Edge Worker host loads a successful rebuild on its next request. When the
rebuild changes the Session runtime, warm subprocesses are retired; their
logical Sessions remain open and start the rebuilt Session runtime on the next
Message. Webhook and homepage edits leave running Sandboxes in place. A failed
rebuild is reported and the last successful build keeps running.

`keepAliveSeconds`, keyed Session opening, cancellation, Sandbox reactivation,
Message dispatch, request/reply, asynchronous Session output, bounded replay,
SSE, and output-only WebSockets use the public SDK routes and semantics. Releasing
a Session Sandbox is idempotent and leaves its logical Session usable.
Message dispatch acknowledges before Sandbox startup and continues on the local
runner's background context. Messages are delivered in acceptance order within
one logical Session while different Sessions may run concurrently. The next
command is admitted after the preceding runtime acknowledgement. Harness input
and control remain responsive while provider work is active; a custom `receive`
handler should move long-running work into supervised activity.
Request timeouts and disconnected callers stop waiting without canceling the
accepted work; retrying the same ID retrieves its original reply.

The Sandbox remains protected until generation-fenced quiescence **and output
acknowledgment**, then begins its keep-alive interval. Status includes the SDK
execution projection. Handler failures preserve unrelated background activity;
expired work is canceled and bounded by the same grace periods as remote execution.
Lost admission acknowledgments are reconciled against the original Sandbox and
fixed execution deadline. Opt-in `onRecover` runs on a replacement after a local
Sandbox process crash, without replaying the interrupted Message. Rebuilds,
explicit releases, and host-enforced termination do not trigger recovery.

The local and remote handlers run one shared public contract suite. Release
qualification also runs the actual SDK client against the native local runtime,
covering active-activity commands, replies, failures, timeouts, duplicate retries,
ACK loss, multi-batch final output, and crash recovery. Set
`CANTELOP_SDK_RUNTIME_TEST_MODULE` to a built SDK `dist/runtime.js` to require this
integration test; an invalid explicit path fails instead of skipping it.
Database qualification similarly sets `CANTELOP_SDK_DATABASE_TEST_MODULE` to the
built SDK's `dist/database.js` and exercises SQL types, batches, transactions,
concurrent writes, credential renewal, App/Session sharing, and native restarts.
`CANTELOP_LOCAL_DATABASE_CONTAINER_TEST=1` adds the real Docker roundtrip; release
qualification requires it.

Restarting `cantelop dev` loses pending work, Message status, cached replies,
and recovery identities. Committed views, stream identity, and bounded event
replay persist in encrypted local state under `.cantelop/dev/<app>/`. Durable
views are not yet available on hosted Cantelop. It does not reproduce remote durable queue/reply retention,
distributed failover, hosted credentials, or production infrastructure. Workspace files
persist, but protocol parity tests are not a guarantee of identical platform
behavior. Use container mode for Linux/runtime-image compatibility checks.
Workspace databases work in both native and container mode. The CLI creates one
SQLite database per Workspace under `.cantelop/dev/<app>/databases/`. App code
outside the Sandbox, meaning your backend and the App's webhook and homepage
handlers, opens it with `workspace.database()`; every Session in that Workspace
opens the same database with `context.database()`. Parameterized SQL, atomic
batches, multiple-statement execution, and interactive transactions use the same
SDK interfaces as a deployment. Databases persist across
rebuilds, Session restarts, and separate development runs; each App/Workspace
identity gets its own file. No login, Turso account, or hosted credentials are needed.

These databases are disposable local development data. Stop `cantelop dev` and
remove `.cantelop/dev/<app>/databases/` to reset all local Workspace databases while
retaining Workspace files. Nothing is synced to or deleted from hosted databases.
Local credentials expire after 15 minutes and are renewed automatically. Session
shutdown blocks new credential requests; issued SQL credentials remain usable until
expiry or runner shutdown. Open database streams are rolled back and closed after
one minute of inactivity, including abandoned transactions. SQL operations are never
replayed automatically.

Native mode serves the database bridge on numeric loopback. Container mode binds
its bridge to host interfaces so Docker Sandboxes can reach it through
`host.docker.internal` (with Docker's host gateway mapping). Bridge operations require
a private development token; database access requires a Workspace-scoped bearer
token, and browser-origin requests to the SQL service are rejected. The CLI injects
all local endpoint and token settings automatically; do not add them to `.env`.

Workspace references are lazy. `app.workspace({ slug }).resolve()` explicitly
materializes one idempotently; dispatch and request also resolve a slug on first
use. Canonical ID reads never create a replacement. Every Session names its
Workspace by ID or slug; there is no default Workspace. Workspace IDs are stable
for an App and slug, and files live under
`.cantelop/dev/<app>/workspaces/` beneath the project root. Projects should ignore
`.cantelop/` in source control.

Use container parity mode when validating native libraries, Linux behavior, or
container filesystem assumptions:

```sh
cantelop dev --container
```

Container mode builds the Session runtime for `linux/amd64`, runs each Sandbox in a
private Docker container, and bind-mounts its persistent Workspace at
`/workspace`. Both container and hosted Sessions start there as the managed
identity `10001:10001`. The CLI supplies the pinned Bun loader and supporting
libraries, including when the customer base uses musl. It performs a one-time
build; restart the command after source changes. Native mode sets each
subprocess's working directory and `CANTELOP_WORKSPACE_PATH` to the host
Workspace directory. Container and hosted Sessions set
`CANTELOP_WORKSPACE_PATH=/workspace`. Native mode does not emulate the container
image, Linux, `/workspace`, or the container filesystem.

The CLI prepares local Workspace permissions automatically. The mount root keeps
its host owner and group, and local containers receive that supplementary group.
Setgid directories and a local umask of `0002` let the host and Session share new
files. Existing descendants are not recursively changed. Local Docker shutdown
sends SIGTERM and waits up to five seconds before Docker terminates the process.

The development runner deliberately does not start fire-fuse. Local process or
Docker isolation replaces Firecracker, the Edge Worker runs in a Node.js Web API
host rather than Cloudflare workerd, and distributed recovery, production
networking, snapshots, admission control, and distributed event replay are
outside this local boundary. The loopback bridge does implement the production
event envelope and transport behavior, including bounded persisted event
replay. It does not preserve pending application work across runner restarts.

Native mode requires Node.js 22+, SDK 1.0, and Bun 1.3.10. Container mode
requires Docker with `linux/amd64` build support; the CLI supplies its internally
pinned Bun runtime.

## Deploy a complete release

`cantelop init` produces schema 3. All paths are relative to the manifest:

```json
{
  "schema_version": 3,
  "definition": "src/cantelop.ts"
}
```

The definition exports named Apps made with `CantelopClient`. Each App's literal
`name` is its deployment slug. Select one with `--app NAME` when the module
exports multiple Apps. Generated `app_...` IDs do not belong in project source.

Runtime, configuration, and image declarations belong in the App definition:

```ts
export const agent = cantelop.app<{ prompt: string }>({
  name: "vera",
  runtime: { receive },
  environment: {
    OPENAI_MODEL: { default: "gpt-4.1-mini" },
    CODEX_API_KEY: { secret: true, required: true },
  },
});
```

Environment declarations do not set production credentials. `cantelop doctor`
checks required names and secret/variable kinds against the authenticated App
without printing secrets. Non-secret defaults reach generated Edge code and
the runtime image; `.env` overrides them locally. Set production values with
configuration commands or `deploy --env-file`. Names beginning with
`CANTELOP_` are reserved. Secret declarations cannot contain defaults.

Run the complete local release build as a pre-flight without logging in or
contacting the Cantelop control plane:

```sh
cantelop build
```

The command validates `cantelop.json` and the SDK build protocol, then compiles
the selected App into the generated Edge Worker, Session runtime, and backend
client in one SDK build. It checks that the Edge Worker and Session runtime
share the App's name, runtime identity, environment declarations, and
`dockerfile`, and prints the Edge Worker's routes. It then builds the declared or
Cantelop-provided base image for `linux/amd64` and layers the platform Session
runtime into the final image. Temporary build artifacts are removed afterwards.
Docker may still pull base images or execute network-dependent Dockerfile steps;
no release is uploaded.

Deploy the complete project with one command:

```sh
cantelop deploy
```

To import production values according to the `environment` declarations in
the App definition and deploy in one command:

```sh
cantelop deploy --env-file .env
# Validate and preview names and storage kinds without changing the App:
cantelop deploy --env-file .env --dry-run
```

The file path is relative to the directory containing `cantelop.json` (or the
manifest selected with `-config`); absolute paths also work. Only declared names
are imported. Entries marked `secret: true` become secrets; other entries become
ordinary variables. Undeclared file entries are ignored. Every `required: true`
name must be present in the file, even if the App already has a value. An explicit
empty value counts as present. Missing optional entries leave existing App values
unchanged; declared defaults are never imported.

The CLI validates the file and selected values before building or changing the
App. After a successful build, it resolves or creates the App, applies the
configuration, and uploads the release. Configuration updates are individual
requests: failure stops deployment, but earlier updates remain applied. A later
deployment failure does not roll back configuration. Existing variable/secret
kind conflicts must be resolved with the configuration commands. Dry runs print
names and storage kinds only and make no configuration changes. Without
`--env-file`, deployment does not read `.env`.

If the selected App does not exist, an interactive deployment asks before
creating it. Non-interactive deployments must opt in explicitly:

```sh
cantelop deploy --create-app
```

The deploy spelling also supports the same pre-flight path:

```sh
cantelop deploy --dry-run
```

Dry-run parses and builds before credential loading, requires no Cantelop
login, performs no control-plane requests, and prints the qualified Session runtime
image ID. It exercises the same build implementation used by a real deploy;
only authentication and release upload/activation are skipped.

Use `-config PATH` when the configuration has a different name or location.
The CLI invokes the project-installed `@cantelop/sdk/build` to compile the App
definition, then bundles the SDK Session runtime into one module for the
platform Bun. By default, Cantelop builds a security-patched Debian image with
CA roots. For both default and custom images, the CLI supplies a managed user
(`10001:10001`), fixes the runtime working directory at `/workspace`, adds the
Session module and runtime libraries, disables inherited health checks, and sets
the startup command and stop signal. Customer `USER`, `WORKDIR`, `ENTRYPOINT`, and `CMD` settings do not
control Session startup. They may still be used inside Dockerfile build steps.

Add `dockerfile` to `cantelop.app(...)` only when the Session runtime needs native dependencies
or system tools. The SDK bundles the Session module before image construction;
customers do not need to install Bun or write a launcher. SDK bundling is the
only Session runtime launch path.

The CLI supplies writable, ephemeral `HOME`, `TMPDIR`, `TMP`, `TEMP`, and XDG
cache/config/data/state directories under `/__cantelop`. These environment
values, along with `USER` and `LOGNAME`, take precedence over image and App
configuration. Durable files belong under `/workspace`; package installation
and image assets belong in system directories or a path such as `/opt/app`.

The final base image must not declare `VOLUME`, pending `ONBUILD` triggers,
`CANTELOP_*` environment variables, or files at `/__cantelop`, `/opt/cantelop`,
or underneath `/workspace`. The CLI checks the built image, including inherited
metadata, before adding its runtime layer. An empty `/workspace` directory is
allowed. Images do not need an entrypoint or command for this inspection.
`EXPOSE` is informational; Cantelop assigns the Session's listener port.

For a custom image, declare its Dockerfile on the App:

```ts
export const agent = cantelop.app<{ prompt: string }>({
  name: "vera",
  runtime: { receive },
  dockerfile: "docker/Dockerfile",
});
```

Custom images always use the directory containing `cantelop.json` as the Docker
build context, including when the Dockerfile is in a subdirectory. `COPY` paths
are relative to that directory; put `.dockerignore` in the project root.

The CLI also writes the compiled backend client to `.cantelop/client/`. Deploy
the complete directory, including declarations and `types/`, with your backend.
Runtime logic or type changes produce a new identity, and an older backend gets
`session_runtime_mismatch` (409) on Session commands. Rebuild and deploy the
backend and App from the same source; restore a matching backend when rolling
back. See the [SDK runtime compilation guide](https://github.com/stepandel/cantelop-sdk/blob/main/docs/runtime-definitions.md#deploy-the-backend-with-the-app).

The CLI resolves the final immutable image ID and computes both SHA-256 digests
before making the deployment request.
Build output is temporary and is removed after deployment. The control API
returns short-lived, App-bound upload targets. The Edge Worker bundle and the
multipart Session runtime image go directly to private S3-compatible object
storage; they never pass through the Next.js process.

After both uploads finish, the CLI finalizes the bundle once. The server creates
one chronological `rel_...` release with two child operations: `api` for the
Edge Worker and `session_runtime` for the Session runtime image. The CLI polls
that release and reports both component states, labelled `API` and
`Session runtime`. The App's active release changes only when both children
succeed; either failure fails the whole release and prevents partial
activation. Cloudflare credentials, compute-agent credentials, dispatch
namespace selection, and provider metadata remain platform configuration.

There is no Edge Worker-only deployment, Session runtime-only update, standalone
upload, Build-ID argument, or version argument. `cantelop deploy` is the
customer deployment mutation; the control plane assigns release and Build
identity. The lower-level `cantelop app deploy APP_ID -image IMAGE -artifact DIRECTORY`
command serves automation that already builds the Edge Worker artifact
directory and the Session runtime image. It reaches the same complete-release
endpoint and cannot activate either component independently.

## Operate a deployed App

Operational commands use the selected App's name from the definition in `cantelop.json`, so generated App
IDs do not need to be copied into scripts. Use `-config PATH` to target another
manifest.

```sh
cantelop routes
cantelop routes --openapi > openapi.json
cantelop logs
cantelop logs --follow
cantelop sandboxes
cantelop traces
cantelop session list
cantelop session inspect SESSION_ID
cantelop session terminate SESSION_ID
cantelop releases
cantelop rollback
```

`cantelop sandboxes` reads the same live App-scoped runtime snapshot as the
console's Sandboxes page. The table includes state, Workspace, Build, profile,
CPU/memory allocation, current memory usage, cumulative CPU usage, startup time,
and failure reason. `cantelop sandboxes SANDBOX_ID` shows every field for one
Sandbox, including observation and lifecycle timestamps. `--json` without an ID
returns the full snapshot, including readiness, Workspaces, and Sessions; with
an ID it returns that Sandbox object. CPU usage is cumulative microseconds, not
an instantaneous utilization percentage.

`cantelop traces` reads the same recent trace tree as the console's Traces page:
requests, messages, Sessions, execution attempts, Sandboxes, spans, logs, errors,
timing, and attributes. Human output is an indented tree; `--json` retains the
complete API document. Supply a request, message, attempt, trace, or span ID to
select matching roots while retaining their surrounding request context:

```sh
cantelop sandboxes SANDBOX_ID --json
cantelop traces TRACE_ID
cantelop traces --json
```

Both commands select the App from `cantelop.json` and accept `-config PATH`.
They read a snapshot per invocation. Trace results use the console's bounded
recent window (100 request/message roots); ID lookup filters that window and
does not search older history. These commands require the console version with
the `/v1/apps/APP_ID/sandboxes` and `/v1/apps/APP_ID/traces` endpoints.

Logs are durable completed-request events with timestamp, HTTP status,
duration, Session, and Workspace. `--follow` prints the current bounded tail
oldest-first, then reads forward from the last printed event each second, so a
burst larger than one page is printed in full. Against a console that predates
forward reads it falls back to polling the newest page. Session termination releases the
current Sandbox; the logical keyed Session remains reusable and becomes idle.

`cantelop routes` lists the routes the live release declares, each with its
request count, errors, median latency, and last request over the observability
retention window, then any path that received requests without being declared.
It reads the same overview as the console's Ingress page and requires the console
version with the `/v1/apps/APP_ID/routes` endpoint. SDK 1.0 publishes the
generated command paths, declared webhooks, and optional homepage in its Edge
manifest.

`cantelop routes METHOD PATH`, for example `cantelop routes POST /commands/dispatch`, shows
one route: its status distribution with p50 and p95 latency, then what became
of the Session work its requests started, including attempt outcomes, time to
start and finish, failure reasons, and recent failing requests with their
request and trace IDs. The route table's `FAILED WORK` column is the same
measure in brief, as failed attempts over messages dispatched. A `202` says a
message was accepted; these numbers say whether the work behind it succeeded.

`cantelop routes --openapi` writes the declared routes as an OpenAPI 3.1
document with the App's public origin as its server, ready to import into an
API client such as Postman. The platform knows each route's method and path
only. Parameters, request and response bodies, and the authorization each route
expects are left open, never invented, and undeclared traffic is left out. The
command fails when the live release declares no routes.

`cantelop releases` prints newest releases first. `cantelop rollback`
atomically activates the newest superseded release and refuses to race a
deployment that is still queued or provisioning. Running rollback again can
reactivate the release that was just superseded. Roll back the backend to its
matching compiled client too.

Every command in this section accepts `--json`. Finite commands emit one JSON
document matching the control API. `cantelop logs --follow --json` emits one
request-event object per line (NDJSON), which is safe to pipe through streaming
processors such as `jq --unbuffered`.

## Variables and secrets

Applications can update their declared environment through SDK 1.4.0's
`app.environment().set`, `unset`, `status`, and `wait` methods without invoking
this CLI or using tenant administrative credentials. The App Edge authenticates
its integration credential, then the outbound broker and gateway bind the
mutation to the trusted App identity. Only declared names of the matching kind
are writable; secrets remain write-only and required values cannot be removed.
The same encrypted configuration store and `sync_config` worker apply updates
without a release. Callers can wait for the returned revision to reach Edge and
compute before starting a new Session. Active Sandbox process environments keep
their existing values until those Sandboxes stop.

Sync both variables and secrets from the project's `.env` without building or
deploying:

```sh
cantelop env sync
cantelop env sync --env-file .env.production
cantelop env sync --env-file .env.production --dry-run
```

This uses the same manifest selection, required-name checks, secret handling,
and individual updates as `cantelop deploy --env-file`. It resolves the existing
App by the name declared in the App definition; use `-config` to select another
manifest.
The default file is `.env`, relative to the manifest directory. Dry runs require
no login and show names and storage kinds only. Omitted optional values are left
unchanged; sync does not delete configuration or create an App. Changes affect
the App's Edge Worker and newly started Sandbox VMs without creating a release.

Set normal variables directly and supply secret values through standard input
so they do not appear in shell history or the process argument list:

```sh
cantelop app env set app_0123456789abcdef0123456789abcdef LOG_LEVEL=debug
printf %s "$DATABASE_URL" |
  cantelop app secret set app_0123456789abcdef0123456789abcdef DATABASE_URL
```

Set multiple ordinary variables at once, or load a file of variables or secrets
before deploying:

```sh
cantelop app env set APP_ID LOG_LEVEL=debug FEATURE_ENABLED=true
cantelop app env set APP_ID --env-file .env.production
cantelop app secret set APP_ID --env-file .env.secrets
cantelop deploy
```

Each file entry is stored as the selected kind: `env` creates ordinary variables,
while `secret` stores every entry as a secret. Files use the same format as
`cantelop dev`: `NAME=VALUE` lines, optional `export`, blank lines, comments, and
single or double quoted values. Values are not expanded from the shell.
Duplicate names and reserved `CANTELOP_` names are rejected. Files are loaded only
when explicitly supplied; deployment does not automatically import `.env`.

The CLI validates the entire batch before sending updates, then applies names
in sorted order and prints only their names. Updates are separate API requests,
not an atomic transaction. On failure, it stops and reports the failed name and
number of confirmed updates; previous updates remain applied, and the failed
request may have applied if its response was lost. Retry the command after
resolving the error. Names omitted from a batch are left unchanged.

List or remove values with the corresponding commands:

```sh
cantelop app env list APP_ID
cantelop app env unset APP_ID LOG_LEVEL
cantelop app secret list APP_ID
cantelop app secret unset APP_ID DATABASE_URL
```

Variable listings print `NAME=value`. Secret listings print names only; the
control plane never returns secret values. A name cannot be both a variable and
a secret. Unset it before changing its kind.

App configuration is shared by the App's Edge Worker and every newly started
Sandbox VM. Docker image `ENV` values are VM defaults and App configuration
overrides ordinary application defaults. Managed Session environment values
remain platform-owned. Each mutation replaces the Edge Worker's complete
customer binding set before the control plane makes the value active for new
VMs. A failed Worker projection leaves the prior App configuration active and
can be retried with the same command. Configuration changes do not restart or
modify Sessions that are already running.

## Call a deployed App from your backend

`cantelop deploy` creates the App's integration credential and records it in
the local `default` SDK profile. A backend hosted elsewhere (Railway, Fly,
Render, Vercel, and so on) cannot read that profile, so give it the credential
through the host's secret store:

```sh
cantelop app credentials --app my-agent
```

The default `dotenv` format prints `CANTELOP_APP_SLUG` and
`CANTELOP_INTEGRATION_TOKEN`, plus `CANTELOP_EDGE_URL` when the App is not
served at `https://<slug>.cantelop.dev`. `--format app-config` prints one line
of JSON for `CANTELOP_APP_CONFIG`, and `--format token` prints only the token.
Without `--app`, the command uses the project's App. Credentials go to stdout
and status to stderr, so the output can be piped into another CLI:

```sh
railway variables --set "CANTELOP_APP_SLUG=my-agent" \
  --set "CANTELOP_INTEGRATION_TOKEN=$(cantelop app credentials --app my-agent --format token)"
cantelop app credentials --app my-agent | fly secrets import
```

The token authorizes every Workspace and Session of the App. Keep it on the
backend, never in browser code, and authenticate your own users before choosing
their Workspace. An App has credentials after its first deployment; before that
the command reports that none exist.

### Rotate the token

```sh
cantelop app credentials rotate --app my-agent
cantelop app credentials rotate --app my-agent --grace 2h
cantelop app credentials rotate --app my-agent --now
cantelop app credentials rotate --app my-agent --retry
```

Rotation issues a new token and prints it like `app credentials`. The previous
token keeps working for the grace period (`--grace`, 24 hours by default, at
most 168 hours), so update the backend's secret and restart it within that
window. `--now` stops the previous token as soon as the Edge has the new one;
use it when a token leaked. After a rotation reaches Edge, rotating again during
its grace period replaces the previous token, so only the most recent two tokens
ever work. A pending or failed rotation must be retried with `--retry` before
issuing another token. Retrying resends the existing token and preserves the
original grace-period expiry; it does not generate another credential.

The command waits until the Edge has the new token, then updates the App in the
local `default` SDK profile if it is recorded there. `--no-wait` returns
immediately and leaves the local profile unchanged while propagation is pending;
check `cantelop app credentials` before moving callers. A wait timeout exits with
an error and preserves the local profile. Edge acceptance is recorded separately
from compute synchronization, so a subsequent compute failure does not hide a
successful token rotation. If Edge propagation fails or is uncertain, use
`cantelop app credentials rotate --app my-agent --retry` to resend the same token.
A failed wait preserves the local profile; do not assume which token Edge accepts
after an uncertain provider response.

The generated Edge Worker accepts both the current and the previous token
during a grace period. Local `cantelop dev` credentials are separate and are
regenerated by each `cantelop dev` run.

## Accepted images and failure behavior

The source must resolve to exactly one standard uncompressed `docker image
save` archive for `linux/amd64`. The CLI's final image carries the managed
Session runtime contract; the customer's Dockerfile only supplies the base
environment. The host validates the fixed identity, working directory, startup
command, and runtime environment before materializing the image without
starting customer code. The CLI marks the image with Session runtime contract
version 1, and the host rejects any other contract version.

Uploads have no whole-request timeout and never pass through Next.js. Metadata
requests remain bounded, short-lived tenant access tokens are refreshed during
long operation polling, and the configured product API origin is pinned at
login so a later token exchange cannot redirect archive bytes elsewhere.
The CLI stores a private in-flight deployment identity beside its credentials;
rerunning the same App and artifact bundle after an interruption resumes the
same upload or release. Terminal completion removes that retry state, so a
later intentional deployment of identical artifacts still creates a new release.

Only one upload may be pending for an App. Failed uploads consume no version;
successful versions are sequential within that App beginning at 1.

### Cancel an abandoned deployment upload

An interrupted upload can hold the App's upload lock for up to one hour.
Cancel it immediately without rebuilding or reproducing the Docker archive:

```sh
cantelop deploy cancel
# Without a local project, use an App ID or slug:
cantelop deploy cancel --app my-app
# Target a specific upload, including when retrying storage cleanup:
cantelop deploy cancel --app my-app --upload upl_0123456789abcdef0123456789abcdef
```

The command accepts `--config PATH`, discovers the pending upload when
`--upload` is omitted, and cancels only that upload ID. It clears the matching
local retry identity so a subsequent `cantelop deploy` starts a fresh request.
Rebuilding an image can change the Docker archive digest; cancellation does not
require the original archive or retry state.

Cancellation releases the upload lock before attempting to abort the multipart
upload. If storage cleanup fails, the CLI prints a cleanup retry command, but
new deployments can still proceed. Already-uploaded staging objects and signed
API upload URLs are not deleted or revoked; the cancelled upload cannot create
a release. Repeated cancellation is safe and cannot cancel a newer upload.

Without `--release`, the command only cancels uploads. If finalization wins a
race with upload cancellation, cancellation returns a conflict and leaves the
release unchanged. Use `cantelop releases` to inspect releases and
`cantelop rollback` to restore a previous active release.

### Cancel or restart a release

A release that has not become active can be cancelled by ID, and a cancelled
or stuck release can be restarted from its already-uploaded artifacts without
rebuilding:

```sh
cantelop deploy cancel --release rel_0123456789abcdef0123456789abcdef
cantelop deploy restart --release rel_0123456789abcdef0123456789abcdef
```

Both commands accept `--app APP_ID_OR_SLUG` and `-config PATH`; `--release`
cannot be combined with `--upload`. Cancellation moves the release to
`cancelling`, and the App stays locked until provider cancellation completes.
Restart performs the same cancellation and then starts a new release from the
uploaded artifacts; repeating it for the same release returns that new release.
Active and superseded releases cannot be cancelled or restarted, and either
command returns a conflict while another release for the App is queued,
provisioning, or cancelling, or while a release activation is in progress.
