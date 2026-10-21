package devruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stepandel/cantelop/clients/cli/internal/deploy"
	"github.com/stepandel/cantelop/clients/cli/internal/integration"
	"github.com/stepandel/cantelop/clients/cli/internal/projectconfig"
)

func TestSDKOneAppArtifacts(t *testing.T) {
	sdk := os.Getenv("CANTELOP_SDK_DIRECTORY")
	if sdk == "" {
		t.Skip("set CANTELOP_SDK_DIRECTORY to qualify the real SDK")
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "node_modules", "@cantelop"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(sdk, filepath.Join(root, "node_modules", "@cantelop", "sdk")); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"type":"module"}`), 0o644)
	definition := filepath.Join(root, "cantelop.ts")
	source := `import {CantelopClient} from "@cantelop/sdk";import {claudeAuth} from "@cantelop/sdk/harnesses/claude";const cantelop=new CantelopClient();export const one=cantelop.app({name:"agent-one",auth:claudeAuth,runtime:{receive(){}},homepage:()=>new Response("hello")});export const two=cantelop.app({name:"agent-two",runtime:{receive(){}}});`
	os.WriteFile(definition, []byte(source), 0o644)
	configPath := filepath.Join(root, "cantelop.json")
	os.WriteFile(configPath, []byte(`{"schema_version":3,"definition":"cantelop.ts"}`), 0o644)
	if _, err := projectconfig.Load(configPath); err == nil {
		t.Fatal("multiple Apps must require selection")
	}
	config, err := projectconfig.LoadSelected(configPath, "agent-one")
	if err != nil {
		t.Fatal(err)
	}
	if config.AppSlug != "agent-one" || config.RuntimeID == "" || config.APIEntrypoint != "" {
		t.Fatalf("config %#v", config)
	}
	out := filepath.Join(root, "artifacts")
	edge := filepath.Join(out, "api")
	runtime := filepath.Join(out, "session-runtime")
	command := exec.Command("node", "--input-type=module", "--eval", deploy.SDKBuildProgram, definition, edge, config.AppSlug, runtime, root)
	command.Dir = root
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v", output, err)
	}
	artifact, err := projectconfig.LoadAPIArtifact(edge)
	if err != nil {
		t.Fatal(err)
	}
	if err := projectconfig.ValidateRuntimePair(artifact.Manifest, runtime); err != nil {
		t.Fatal(err)
	}
	if artifact.Manifest.Auth == nil || !*artifact.Manifest.Auth {
		t.Fatal("native auth declaration lost")
	}
	routes, _, _ := artifact.Manifest.DeclaredRoutes()
	if len(routes) != len(projectconfig.AppCommandTypes)+2 {
		t.Fatalf("routes %#v", routes)
	}
	for _, commandType := range projectconfig.AppCommandTypes {
		found := false
		for _, route := range routes {
			if route.Method == "POST" && route.Path == "/commands/"+commandType {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("missing command route %q in %#v", commandType, routes)
		}
	}
	if _, err := os.Stat(filepath.Join(root, ".cantelop", "client", "client.mjs")); err != nil {
		t.Fatal(err)
	}
	document, _ := os.ReadFile(filepath.Join(runtime, "cantelop-runtime.json"))
	var input map[string]any
	json.Unmarshal(document, &input)
	capabilities := input["capabilities"].(map[string]any)
	delete(capabilities, "authTerminal")
	document, _ = json.Marshal(input)
	os.WriteFile(filepath.Join(runtime, "cantelop-runtime.json"), document, 0o644)
	if err := projectconfig.ValidateRuntimePair(artifact.Manifest, runtime); err == nil {
		t.Fatal("mismatched auth declarations accepted")
	}
	capabilities["authTerminal"] = false
	document, _ = json.Marshal(input)
	os.WriteFile(filepath.Join(runtime, "cantelop-runtime.json"), document, 0o644)
	if err := projectconfig.ValidateRuntimePair(artifact.Manifest, runtime); err == nil {
		t.Fatal("false auth capability accepted")
	}
	capabilities["authTerminal"] = true
	input["session_runtime_id"] = "different"
	document, _ = json.Marshal(input)
	os.WriteFile(filepath.Join(runtime, "cantelop-runtime.json"), document, 0o644)
	if err := projectconfig.ValidateRuntimePair(artifact.Manifest, runtime); err == nil {
		t.Fatal("mismatched artifacts accepted")
	}
}

func TestIntegrationProfilesRemainPrivateAndPerApp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "integration.json")
	t.Setenv("CANTELOP_INTEGRATION_CONFIG", path)
	for _, name := range []string{"one", "two"} {
		if err := integration.SaveApp("default", integration.AppConfiguration{ID: projectconfig.LocalAppID(name), Slug: name, AccessToken: strings.Repeat(name, 32), RuntimeID: name}); err != nil {
			t.Fatal(err)
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatal("credential file is not private")
	}
	document, _ := os.ReadFile(path)
	var config integration.Configuration
	if json.Unmarshal(document, &config) != nil || len(config.Profiles["default"].Apps) != 2 {
		t.Fatal("App credentials overwritten")
	}
}

func TestSDKOneLocalWorkerAndSandbox(t *testing.T) {
	sdk := os.Getenv("CANTELOP_SDK_DIRECTORY")
	if sdk == "" {
		t.Skip("set CANTELOP_SDK_DIRECTORY to qualify the real SDK")
	}
	root := t.TempDir()
	t.Setenv("CANTELOP_INTEGRATION_CONFIG", filepath.Join(root, "integration.json"))
	os.MkdirAll(filepath.Join(root, "node_modules", "@cantelop"), 0o755)
	if err := os.Symlink(sdk, filepath.Join(root, "node_modules", "@cantelop", "sdk")); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"type":"module"}`), 0o644)
	os.WriteFile(filepath.Join(root, "homepage.ts"), []byte(`export function homepage(){return new Response("hello");}`), 0o644)
	definition := filepath.Join(root, "cantelop.ts")
	source := `import {CantelopClient} from "@cantelop/sdk";import type {SessionHarnessContext,SessionHarness,SessionHarnessWorkContext} from "@cantelop/sdk/session";import {homepage} from "./homepage.js";
function create(context:SessionHarnessContext):SessionHarness<{prompt:string},{text:string;pid:number},{text:string},{text:string}>{
 let active:SessionHarnessWorkContext<{prompt:string},{text:string;pid:number},{text:string}>|undefined;
 let controller:AbortController|undefined;
 let waiting=Promise.resolve();
 let steering=Promise.resolve();
 const harness:SessionHarness<{prompt:string},{text:string;pid:number},{text:string},{text:string}>={
  queue(message,work){
   const completion=waiting.then(async()=>{
    controller=new AbortController();
    const signal=AbortSignal.any([work.signal,controller.signal]);
    active={...work,signal};
    try {
     signal.throwIfAborted();
     if(message.prompt==="hold"){await new Promise(resolve=>signal.addEventListener("abort",resolve,{once:true}));signal.throwIfAborted();}
     await work.output.send({text:message.prompt,pid:process.pid});await work.view.commit({text:message.prompt});return {text:message.prompt};
    } finally {await steering;active=undefined;controller=undefined;}
   });
   waiting=completion.then(()=>{},()=>{});
   return completion;
  },
  async steer(message,work){
   work.signal.throwIfAborted();
   if(!active){await harness.queue(message,work);return;}
   const target=active;
   const delivery=steering.then(async()=>{await target.output.send({text:message.prompt,pid:process.pid});await target.view.commit({text:message.prompt});});
   steering=delivery.catch(()=>{});
   await delivery;
  },
  cancel(){if(!controller)return false;controller.abort();return true;}
 };
 return harness;
}
const cantelop=new CantelopClient();export const agent=cantelop.app<{prompt:string},{text:string;pid:number},{text:string},{text:string}>({name:"local-agent",runtime:{create},auth:{codeLogin:{link(){return undefined},promptCount(){return 0},input(code){return code}},async status(){return true},async logout(){},terminal(){throw new Error("unused terminal")}},homepage,webhooks:{"/webhooks/test":{method:"POST",async handle({app}){return Response.json(await app.workspace({slug:"webhook"}).session({id:"hook"}).dispatch({prompt:"webhook"}));}}}});`
	os.WriteFile(definition, []byte(source), 0o644)
	configPath := filepath.Join(root, "cantelop.json")
	os.WriteFile(configPath, []byte(`{"schema_version":3,"definition":"cantelop.ts"}`), 0o644)
	config, err := projectconfig.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var runtime *localRuntime
	bridge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { runtime.ServeHTTP(w, r) }))
	defer bridge.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	built, err := buildLocalProject(ctx, config, bridge.URL, false, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer built.cleanup()
	factory, err := newNativeLocalSandboxFactory(built.sessionRuntimeModule, nil, filepath.Join(root, "workspaces"), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err = newLocalRuntime(projectconfig.LocalAppID(config.AppSlug), factory)
	if err != nil {
		t.Fatal(err)
	}
	closeState, err := runtime.enableDurableEvents(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer closeState()
	defer runtime.Close()
	runtime.bridgeToken = strings.Repeat("b", 64)
	_, bindings, hostFile, err := writeLocalRuntimeFiles(built.temporaryRoot, map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	if err := integration.ConfigureLocal(config, port, bindings, bridge.URL); err != nil {
		t.Fatal(err)
	}
	host, err := startLocalAPIHost(ctx, hostFile, built.apiModule, bindings, bridge.URL, runtime.bridgeToken, port, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	origin := fmt.Sprintf("http://127.0.0.1:%d", port)
	response, err := http.Get(origin + "/")
	if err != nil {
		t.Fatal(err)
	}
	document, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if string(document) != "hello" {
		t.Fatalf("homepage %s", document)
	}
	response, err = http.Post(origin+"/webhooks/test", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	document, _ = io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 200 || !strings.Contains(string(document), "msg_") {
		t.Fatalf("webhook %d %s", response.StatusCode, document)
	}
	script := `import assert from 'node:assert/strict';
import {agent} from './.cantelop/client/client.mjs';
const workspace=agent.workspace({slug:'backend'});
assert.equal(await workspace.auth().status(),true);
assert.deepEqual(await workspace.auth().login(),{authenticated:true});
const session=workspace.session({id:'thread'});
assert.deepEqual(await session.request({prompt:'hello'}),{text:'hello'});
const initial=await session.view();assert.deepEqual(initial.state,{text:'hello'});
const events=session.stream();const event=await events.next();assert.equal(event.value.data.text,'hello');await events.return();
await session.stop();assert.deepEqual((await session.view()).state,{text:'hello'});
assert.deepEqual(await session.request({prompt:'again'}),{text:'again'});
const hold=await session.dispatch({prompt:'hold'});
async function statusUntil(ref,state){for(let i=0;i<100;i++){const status=await ref.status();if(status.state===state)return status;await new Promise(r=>setTimeout(r,20));}throw new Error('status did not reach '+state);}
await statusUntil(hold,'handled');
const normal=await session.dispatch({prompt:'normal'});
const queued=await session.dispatch({prompt:'queued'});
const priority=await session.steer({prompt:'priority'});
await statusUntil(priority,'handled');assert.equal((await hold.status()).state,'handled');
assert.equal((await session.cancel()).state,'requested');await statusUntil(normal,'handled');
await statusUntil(queued,'handled');
for(let i=0;i<100;i++){const status=await queued.status();if(status.execution?.phase==='quiescent')break;if(i===99)throw new Error('activity did not quiesce');await new Promise(r=>setTimeout(r,20));}
assert.equal((await session.cancel()).state,'idle');
const resumed=session.stream({after:initial.cursor});let text=[],pids=[];
for(let i=0;i<4;i++){const event=(await resumed.next()).value;text.push(event.data.text);pids.push(event.data.pid);}
assert.equal(new Set(pids).size,1);
assert.deepEqual(text,['again','priority','normal','queued']);await resumed.return();
assert.deepEqual((await session.view()).state,{text:'queued'});
await session.stop();console.log('integration passed');`

	command := exec.CommandContext(ctx, "node", "--input-type=module", "--eval", script)
	command.Dir = root
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("backend %v: %s", err, output)
	}
	// An ingress-only edit rebuilds the generated Worker and preserves runtime identity.
	os.WriteFile(filepath.Join(root, "homepage.ts"), []byte(`export function homepage(){return new Response("updated");}`), 0o644)
	deadline := time.Now().Add(12 * time.Second)
	for {
		response, err = http.Get(origin + "/")
		if err == nil {
			document, _ = io.ReadAll(response.Body)
			response.Body.Close()
			if string(document) == "updated" {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("homepage dependency was not rebuilt")
		}
		time.Sleep(100 * time.Millisecond)
	}
	manifest, err := projectconfig.LoadAPIArtifact(filepath.Dir(built.apiModule))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Manifest.SessionRuntimeID != config.RuntimeID {
		t.Fatal("homepage edit changed runtime identity")
	}
}

func TestIntegrationProfilesSerializeConcurrentAppUpdates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "integration.json")
	t.Setenv("CANTELOP_INTEGRATION_CONFIG", path)
	errors := make(chan error, 10)
	for i := 0; i < 10; i++ {
		go func(i int) {
			name := fmt.Sprintf("agent-%d", i)
			errors <- integration.SaveApp("default", integration.AppConfiguration{ID: projectconfig.LocalAppID(name), Slug: name, AccessToken: strings.Repeat("a", 64), RuntimeID: name})
		}(i)
	}
	for i := 0; i < 10; i++ {
		if err := <-errors; err != nil {
			t.Fatal(err)
		}
	}
	document, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var config integration.Configuration
	json.Unmarshal(document, &config)
	if len(config.Profiles["default"].Apps) != 10 {
		t.Fatal("concurrent App credentials overwritten")
	}
}
