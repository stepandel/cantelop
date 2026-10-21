package devruntime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/messageprotocol"
	"github.com/stepandel/cantelop/fire-fuse/sessionevents"
)

// This test uses both the real SDK client and its runtime, including the native
// dev launcher. The explicit module override makes missing release inputs fail.
func TestLocalSDKMessagingParity(t *testing.T) {
	module := os.Getenv("CANTELOP_SDK_RUNTIME_TEST_MODULE")
	required := module != ""
	if module == "" {
		module = "../../../../../cantelop-sdk/dist/runtime.js"
	}
	module, err := filepath.Abs(module)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(module); err != nil {
		if required {
			t.Fatal(err)
		}
		t.Skip("build adjacent SDK or set CANTELOP_SDK_RUNTIME_TEST_MODULE")
	}
	for _, tool := range []string{"node", "bun"} {
		if _, err = exec.LookPath(tool); err != nil {
			if required {
				t.Fatal(err)
			}
			t.Skip(tool + " required for native SDK qualification")
		}
	}
	directory := t.TempDir()
	script := fmt.Sprintf(`import {createServer} from 'node:http';
import {createSessionRuntimeHandler} from %q;
let calls=0;
const handler=createSessionRuntimeHandler({receive:async ({message,activity,output,reply})=>{
 calls++;
 switch(message.payload.command){
 case 'start': activity.start(async ({signal,output})=>{await output.send({started:true});await new Promise(resolve=>signal.addEventListener('abort',resolve,{once:true}));});break;
 case 'auth': reply({active:activity.active,calls});break;
 case 'fail': throw new Error('intentional handler failure');
 case 'steer': await output.send({steered:true});break;
 case 'cancel': await output.send({final:true});activity.cancel();break;
 case 'slow': await new Promise(resolve=>setTimeout(resolve,150));reply({calls});break;
 case 'output': for(let i=0;i<80;i++)await output.send({index:i});break;
 case 'crash': await output.send({beforeCrash:true});activity.start(()=>new Promise(()=>{}));setTimeout(()=>process.exit(1),30);break;
 }
},onRecover:async ({output,recovery})=>{await output.send({recovered:true,interrupted:recovery.interruptedMessageId,calls});}});
createServer(handler).listen(Number(process.env.CANTELOP_INTERNAL_PORT),'127.0.0.1');`, (&url.URL{Scheme: "file", Path: module}).String())
	runtimePath := filepath.Join(directory, "session-runtime.mjs")
	if err = os.WriteFile(runtimePath, []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(directory, "local-workspace.mjs"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	factory, err := newNativeLocalSandboxFactory(runtimePath, nil, filepath.Join(directory, "workspaces"), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	transport := &lostLocalACKTransport{}
	factory.client = &http.Client{Transport: transport}
	runtime := newTestLocalRuntime(t, factory)
	server := httptest.NewServer(runtime)
	defer server.Close()
	clientModule := filepath.Join(filepath.Dir(module), "remote-app.js")
	clientScript := fmt.Sprintf(`import assert from 'node:assert/strict';import {createRemoteApp} from %q;
const app=createRemoteApp({fetch:r=>fetch(new Request(%q+new URL(r.url).pathname+new URL(r.url).search,r))});
const s=app.sessions.open({id:'parity',workspaceId:%q,keepAliveSeconds:0});
async function settled(ref){for(let i=0;i<500;i++){const status=await ref.status();if(['handled','failed'].includes(status.state))return status;await new Promise(r=>setTimeout(r,10));}throw new Error('message stuck');}
const start=await s.dispatch({command:'start'});const started=await settled(start);
assert.equal(started.execution.outcome,'succeeded');assert.ok(['settled','quiescent'].includes(started.execution.phase));
assert.deepEqual(await s.request({command:'auth'}),{active:true,calls:2});
const failed=await settled(await s.dispatch({command:'fail'}));assert.equal(failed.state,'failed');assert.equal(failed.error.code,'message_handler_failed');
assert.deepEqual(await s.request({command:'auth'}),{active:true,calls:4});
await settled(await s.dispatch({command:'steer'}));
const id='msg_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa';
await assert.rejects(s.request({command:'slow'},{id,timeoutMs:5}),e=>e.code==='request_wait_timeout');
assert.deepEqual(await s.request({command:'slow'},{id,timeoutMs:2000}),{calls:6});
assert.deepEqual(await s.request({command:'slow'},{id,timeoutMs:2000}),{calls:6});
await settled(await s.dispatch({command:'cancel'}));
const out=app.sessions.open({id:'output',workspaceId:%q,keepAliveSeconds:0});await settled(await out.dispatch({command:'output'}));
const recovering=app.sessions.open({id:'recover',workspaceId:'wsp_0123456789abcdef0123456789abcdef',keepAliveSeconds:0});await settled(await recovering.dispatch({command:'crash'}));
console.log('SDK client and runtime parity passed');`, (&url.URL{Scheme: "file", Path: clientModule}).String(), server.URL, localTestWorkspaceID, localTestWorkspaceID)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "node", "--input-type=module", "-e", clientScript).CombinedOutput()
	if err != nil {
		t.Fatalf("SDK client: %v\n%s", err, output)
	}
	// Zero keep-alive must wait for every final event, including batches larger
	// than the protocol's 64-event read limit, before retiring either process.
	for sessionID, count := range map[string]int{"parity": 3, "output": 80} {
		session := runtime.session(localLogicalSessionID(localTestAppID, sessionID))
		if session == nil {
			t.Fatal("missing Session")
		}
		deadline := time.Now().Add(3 * time.Second)
		for {
			session.mu.Lock()
			closed := session.sandbox == nil
			session.mu.Unlock()
			if closed {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("Sandbox did not release after final output")
			}
			time.Sleep(10 * time.Millisecond)
		}
		batch, err := runtime.events.ReadAvailable(sessionevents.Key{WorkspaceID: localTestWorkspaceID, SessionID: sessionID}, 0)
		if err != nil || len(batch.Events) != count {
			t.Fatalf("%s output: %d events, %v", sessionID, len(batch.Events), err)
		}
		for _, event := range batch.Events {
			if !messageprotocol.ValidSandboxID(event.SourceSandboxID) || event.SourceCursor == 0 {
				t.Fatalf("missing output source: %#v", event)
			}
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		batch, err := runtime.events.ReadAvailable(sessionevents.Key{WorkspaceID: localTestWorkspaceID, SessionID: "recover"}, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(batch.Events) >= 2 {
			if len(batch.Events) != 2 || !strings.Contains(string(batch.Events[1].Data), `"recovered":true`) || !strings.Contains(string(batch.Events[1].Data), `"calls":0`) || batch.Events[0].SourceSandboxID == batch.Events[1].SourceSandboxID {
				t.Fatalf("recovery replayed lost handler or used old Sandbox: %#v", batch.Events)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("SDK recovery did not run: %#v", batch.Events)
		}
		time.Sleep(10 * time.Millisecond)
	}

	transport.mu.Lock()
	lost := transport.lost && transport.replyLost
	transport.mu.Unlock()
	if !lost {
		t.Fatal("test did not inject ACK loss")
	}
}

type lostLocalACKTransport struct {
	mu        sync.Mutex
	lost      bool
	replyLost bool
}

func (t *lostLocalACKTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := http.DefaultTransport.RoundTrip(request)
	if err != nil {
		return nil, err
	}
	t.mu.Lock()
	lose := !t.lost && request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/messages")
	if lose {
		t.lost = true
	}
	if !t.replyLost && request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/reply") {
		t.replyLost = true
		lose = true
	}
	t.mu.Unlock()
	if lose {
		_, _ = io.Copy(io.Discard, response.Body)
		response.Body.Close()
		return nil, errors.New("injected lost ACK")
	}
	return response, nil
}
