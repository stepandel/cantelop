package devruntime

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
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

	"github.com/stepandel/cantelop/clients/cli/internal/ids"
)

func testLocalDatabases(t *testing.T, root string) (*localDatabases, *localRuntime, *httptest.Server) {
	t.Helper()
	d, err := newLocalDatabases(root, "", "")
	if err != nil {
		t.Fatal(err)
	}
	runtime := newTestLocalRuntime(t, &fakeLocalSandboxFactory{})
	runtime.databases = d
	runtime.bridgeToken = "test-bridge"
	server := httptest.NewUnstartedServer(runtime)
	d.origin = "http://" + server.Listener.Addr().String()
	d.sandboxOrigin = d.origin
	server.Start()
	t.Cleanup(func() { server.Close(); runtime.Close(); d.Close() })
	return d, runtime, server
}

func databaseHTTPRequest(t *testing.T, d *localDatabases, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	d.ServeHTTP(response, request)
	return response
}

func TestLocalDatabaseAuthorizationAndLifecycle(t *testing.T) {
	d, runtime, _ := testLocalDatabases(t, t.TempDir())
	open := localRuntimeRequest(runtime, http.MethodPost, "/__cantelop/v1/workspaces/open", `{"slug":"database-test"}`)
	// Test helper bypasses the configured bridge authentication only when unset.
	if open.Code != 401 {
		t.Fatalf("unauthenticated bridge status = %d", open.Code)
	}
	runtime.bridgeToken = ""
	open = localRuntimeRequest(runtime, http.MethodPost, "/__cantelop/v1/workspaces/open", `{"slug":"database-test"}`)
	var workspace struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(open.Body.Bytes(), &workspace); err != nil {
		t.Fatal(err)
	}
	response := localRuntimeRequest(runtime, http.MethodPost, "/__cantelop/v1/workspaces/database/credentials", fmt.Sprintf(`{"workspace_id":%q}`, workspace.ID))
	if response.Code != 200 {
		t.Fatalf("credentials: %d %s", response.Code, response.Body.String())
	}
	var credentials map[string]string
	if err := json.Unmarshal(response.Body.Bytes(), &credentials); err != nil {
		t.Fatal(err)
	}
	if response := localRuntimeRequest(runtime, http.MethodPost, "/__cantelop/v1/workspaces/database/credentials", `{"workspace_id":"wsp_11111111111111111111111111111111"}`); response.Code != 404 {
		t.Fatalf("unknown Workspace = %d", response.Code)
	}
	endpoint := "/databases/" + workspace.ID + "/v2/pipeline"
	if response := databaseHTTPRequest(t, d, endpoint, "wrong", `{"requests":[]}`); response.Code != 401 {
		t.Fatalf("invalid token = %d", response.Code)
	}
	if response := databaseHTTPRequest(t, d, "/databases/wsp_11111111111111111111111111111111/v2/pipeline", credentials["authToken"], `{"requests":[]}`); response.Code != 404 {
		t.Fatalf("cross Workspace = %d", response.Code)
	}
	env, revoke := d.sessionEnvironment(workspace.ID)
	response = databaseHTTPRequest(t, d, "/internal/v1/runtime/database/credentials", env["CANTELOP_WORKSPACE_DATABASE_ACCESS_TOKEN"], "")
	if response.Code != 200 {
		t.Fatalf("Session credentials = %d", response.Code)
	}
	revoke()
	if response := databaseHTTPRequest(t, d, "/internal/v1/runtime/database/credentials", env["CANTELOP_WORKSPACE_DATABASE_ACCESS_TOKEN"], ""); response.Code != 401 {
		t.Fatalf("retired Session = %d", response.Code)
	}
	d.mu.Lock()
	grant := d.grants[credentials["authToken"]]
	grant.expires = time.Now().Add(-time.Second)
	d.grants[credentials["authToken"]] = grant
	d.mu.Unlock()
	if response := databaseHTTPRequest(t, d, endpoint, credentials["authToken"], `{"requests":[]}`); response.Code != 401 {
		t.Fatalf("expired token = %d", response.Code)
	}
}

func TestLocalDatabaseAbandonedTransactionRollsBack(t *testing.T) {
	d, _, _ := testLocalDatabases(t, t.TempDir())
	credentials, err := d.apiCredentials(localTestWorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := "/databases/" + localTestWorkspaceID + "/v2/pipeline"
	response := databaseHTTPRequest(t, d, endpoint, credentials["authToken"], `{"requests":[{"type":"sequence","sql":"CREATE TABLE state (value); BEGIN; INSERT INTO state VALUES (1);"}]}`)
	if response.Code != 200 {
		t.Fatalf("pipeline = %d %s", response.Code, response.Body.String())
	}
	var result struct {
		Baton   string `json:"baton"`
		Results []struct {
			Type string `json:"type"`
		} `json:"results"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Results) != 1 || result.Results[0].Type != "ok" {
		t.Fatalf("sequence: %s", response.Body.String())
	}
	d.mu.Lock()
	d.streams[result.Baton].used = time.Now().Add(-2 * time.Minute)
	d.expire(time.Now())
	db := d.databases[localTestWorkspaceID]
	d.mu.Unlock()
	var count int
	if err := db.QueryRow("SELECT count(*) FROM state").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("abandoned write persisted: %d", count)
	}
	if response := databaseHTTPRequest(t, d, endpoint, credentials["authToken"], fmt.Sprintf(`{"baton":%q,"requests":[]}`, result.Baton)); response.Code != 400 {
		t.Fatalf("expired baton = %d", response.Code)
	}
}

// Runs the actual @cantelop/sdk and its libSQL web client against the Go local
// database service. An explicit SDK override is required during qualification.
func TestLocalSDKWorkspaceDatabase(t *testing.T) {
	module := os.Getenv("CANTELOP_SDK_DATABASE_TEST_MODULE")
	if module == "" {
		t.Skip("set CANTELOP_SDK_DATABASE_TEST_MODULE to built SDK dist/database.js")
	}
	module, err := filepath.Abs(module)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(module); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	d, _, server := testLocalDatabases(t, root)
	sdkURL := (&url.URL{Scheme: "file", Path: filepath.Dir(module) + string(os.PathSeparator)}).String()
	script := fmt.Sprintf(`
import assert from 'node:assert/strict';
import {createRemoteApp} from %q;
import {createWorkspaceDatabase} from %q;
import {createSessionDatabase} from %q;
const origin=%q;
const app=createRemoteApp({localDatabaseOrigin:origin,fetch:request=>{const target=new URL(request.url);const headers=new Headers(request.headers);headers.set('X-Cantelop-Dev-Bridge-Token','test-bridge');return fetch(new URL(target.pathname,origin),{method:request.method,body:request.body,duplex:'half',headers});}});
const workspace=await app.workspaces.open({slug:'shared'});
const db=await workspace.database();
await db.executeMultiple('CREATE TABLE state (id INTEGER PRIMARY KEY, value TEXT UNIQUE); CREATE TABLE types (i INTEGER, f REAL, b BLOB, n TEXT);');
await db.execute('INSERT INTO state (value) VALUES (?)',['api']);
assert.equal((await db.execute('SELECT value FROM state')).rows[0].value,'api');
await db.execute({sql:'INSERT INTO types VALUES (:i,:f,:b,:n)',args:{i:42n,f:1.25,b:new Uint8Array([0,1,255]),n:null}});
const types=(await db.execute('SELECT * FROM types')).rows[0];assert.equal(types.i,42);assert.equal(types.f,1.25);assert.deepEqual([...new Uint8Array(types.b)],[0,1,255]);assert.equal(types.n,null);
const batch=await db.batch([{sql:'INSERT INTO state (value) VALUES (?)',args:['batch']},{sql:'SELECT count(*) AS n FROM state',args:[]}],'write');assert.equal(batch[1].rows[0].n,2);
await assert.rejects(db.batch(["INSERT INTO state (value) VALUES ('rolled-back')","INSERT INTO state (value) VALUES ('api')"],'write'));
assert.equal((await db.execute("SELECT count(*) AS n FROM state WHERE value='rolled-back'")).rows[0].n,0);
const tx=await db.transaction();await tx.execute("INSERT INTO state (value) VALUES ('tx-rollback')");await tx.rollback();assert.equal((await db.execute('SELECT count(*) AS n FROM state')).rows[0].n,2);
const committed=await db.transaction();await committed.execute("INSERT INTO state (value) VALUES ('committed')");await committed.commit();
const other=await (await app.workspaces.open({slug:'isolated'})).database();await assert.rejects(other.execute('SELECT * FROM state'));other.close();
let clock=Date.now(),renewals=0;
const renewable=createWorkspaceDatabase(async()=>{renewals++;return {...await db.credentials(),expiresAt:new Date(clock+900000).toISOString()};},{localDatabaseOrigin:origin,now:()=>clock});
const original=await renewable.transaction();await original.execute('SELECT * FROM state');clock+=850000;await renewable.execute('SELECT * FROM state');assert.equal(renewals,2);await original.execute("INSERT INTO state (value) VALUES ('renewed-tx')");await original.commit();renewable.close();
await db.execute('CREATE TABLE concurrent_state (value TEXT)');
const held=await db.transaction();await held.execute("INSERT INTO concurrent_state VALUES ('held')");
const concurrent=db.execute("INSERT INTO concurrent_state VALUES ('concurrent')");
await new Promise(resolve=>setTimeout(resolve,50));await held.commit();await concurrent;
assert.equal((await db.execute('SELECT count(*) AS n FROM concurrent_state')).rows[0].n,2);
console.log(workspace.id);
db.close();
`, sdkURL+"remote-app.js", sdkURL+"database.js", sdkURL+"session.js", server.URL)
	command := exec.Command("node", "--input-type=module", "--eval", script)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("SDK SQL: %v\n%s", err, output)
	}
	workspace := strings.TrimSpace(string(output))
	if !ids.ValidLocalWorkspaceID(workspace) {
		t.Fatalf("invalid Workspace result: %s", output)
	}
	// Run Session access in a real Bun process using the launcher's managed env.
	env, revoke := d.sessionEnvironment(workspace)
	defer revoke()
	sessionScript := fmt.Sprintf(`import {createSessionDatabase} from %q;const db=createSessionDatabase();if((await db.execute('SELECT count(*) AS n FROM state')).rows[0].n!==4)throw new Error('API data not shared');await db.execute("INSERT INTO state (value) VALUES ('session')");db.close();`, sdkURL+"session.js")
	command = exec.Command("bun", "--eval", sessionScript)
	command.Env = localProcessEnvironment(nil, env)
	if output, err = command.CombinedOutput(); err != nil {
		t.Fatalf("Session SQL: %v\n%s", err, output)
	}
	d.mu.Lock()
	db := d.databases[workspace]
	d.mu.Unlock()
	var count int
	if err = db.QueryRow("SELECT count(*) FROM state").Scan(&count); err != nil || count != 5 {
		t.Fatalf("Session write: count=%d error=%v", count, err)
	}
	// A separate service opens the same persisted file after the first closes it.
	d.mu.Lock()
	for baton := range d.streams {
		d.closeStream(baton)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	delete(d.databases, workspace)
	d.mu.Unlock()
	reopened, err := newLocalDatabases(root, server.URL, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reopened.mu.Lock()
	persisted, err := reopened.database(workspace)
	reopened.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err = persisted.QueryRowContext(context.Background(), "SELECT count(*) FROM state").Scan(&count); err != nil || count != 5 {
		t.Fatalf("persisted rows: count=%d error=%v", count, err)
	}
}

func TestProjectDevWorkspaceDatabaseRoundTrip(t *testing.T) {
	module := os.Getenv("CANTELOP_SDK_DATABASE_TEST_MODULE")
	if module == "" {
		t.Skip("set CANTELOP_SDK_DATABASE_TEST_MODULE to qualify the complete local runner")
	}
	sdkRoot := filepath.Dir(filepath.Dir(module))
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "node_modules", "@cantelop"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(sdkRoot, filepath.Join(root, "node_modules", "@cantelop", "sdk")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CANTELOP_INTEGRATION_CONFIG", filepath.Join(root, "integration.json"))
	files := map[string]string{
		"db/schema.ts":  `import {sqliteTable,text} from "@cantelop/sdk/schema";export const state=sqliteTable("state",{value:text()});`,
		"cantelop.json": `{"schema_version":3,"definition":"cantelop.ts"}`,
		"cantelop.ts": `import {CantelopClient} from '@cantelop/sdk';import {receive} from './session.js';
const cantelop=new CantelopClient();export const agent=cantelop.app<{command:string},never,number>({name:'local-database-test',runtime:{receive},homepage:()=>new Response('ready'),webhooks:{'/webhooks/roundtrip':{method:'POST',async handle({app}){
 const workspace=app.workspace({slug:'shared'});const db=await workspace.database();
 const tables=await db.execute("SELECT name FROM sqlite_schema WHERE name LIKE 'cantelop_mailbox_%' OR name='cantelop_system_migrations'");if(tables.rows.length)throw new Error('Mailbox tables were installed');
 await db.execute("INSERT INTO state (value) VALUES ('api')");
 const session=workspace.session({id:'database-session',keepAliveSeconds:0});
 const sessionRead=await session.request({command:'write'},{timeoutMs:15000});
 const total=(await db.execute('SELECT count(*) AS n FROM state')).rows[0].n;
 return Response.json({sessionRead,total});
}}}});`,
		"session.ts": `import type {SessionContext} from '@cantelop/sdk/session';export async function receive({database,reply}:SessionContext<{command:string},never,number>){const db=await database();const count=Number((await db.execute('SELECT count(*) AS n FROM state')).rows[0].n);await db.execute("INSERT INTO state (value) VALUES ('session')");reply(count);}`,
	}
	for name, value := range files {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	modes := []string{"native", "native-restart"}
	if os.Getenv("CANTELOP_LOCAL_DATABASE_CONTAINER_TEST") == "1" {
		modes = append(modes, "container")
	}
	for index, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			port := listener.Addr().(*net.TCPAddr).Port
			_ = listener.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			options := Options{
				ConfigPath: filepath.Join(root, "cantelop.json"), Port: port, Container: mode == "container", EnvironmentPath: ".env",
			}
			done := make(chan error, 1)
			output := &localDatabaseTestOutput{}
			go func() { done <- Run(ctx, options, output) }()
			t.Cleanup(func() {
				cancel()
				select {
				case err := <-done:
					if err != nil {
						t.Errorf("runner shutdown: %v", err)
					}
				case <-time.After(30 * time.Second):
					t.Error("runner did not stop")
				}
			})
			origin := fmt.Sprintf("http://127.0.0.1:%d", port)
			client := &http.Client{Timeout: 20 * time.Second}
			deadline := time.Now().Add(5 * time.Minute)
			for {
				response, err := client.Get(origin + "/")
				if err == nil {
					_ = response.Body.Close()
					if response.StatusCode == 200 {
						break
					}
				}
				select {
				case err := <-done:
					done <- nil
					t.Fatalf("runner startup: %v\n%s", err, output.String())
				default:
				}
				if time.Now().After(deadline) {
					t.Fatal("local runner did not become ready")
				}
				time.Sleep(100 * time.Millisecond)
			}
			response, err := client.Post(origin+"/webhooks/roundtrip", "application/json", strings.NewReader(`{}`))
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatal(err)
			}
			var result struct {
				SessionRead int `json:"sessionRead"`
				Total       int `json:"total"`
			}
			if response.StatusCode != 200 || json.Unmarshal(body, &result) != nil || result.SessionRead != index*2+1 || result.Total != index*2+2 {
				t.Fatalf("roundtrip: status=%d body=%s\n%s", response.StatusCode, body, output.String())
			}
			if mode == "native" {
				// The schema watcher updates already-open Workspace databases.
				updated := `import {sqliteTable,text} from "@cantelop/sdk/schema";export const state=sqliteTable("state",{value:text(),tag:text().default("new")});`
				if err := os.WriteFile(filepath.Join(root, "db", "schema.ts"), []byte(updated), 0600); err != nil {
					t.Fatal(err)
				}
				files, err := filepath.Glob(filepath.Join(root, ".cantelop", "dev", "local-database-test", "databases", "*.sqlite"))
				if err != nil || len(files) != 1 {
					t.Fatalf("Workspace files: %v %v", files, err)
				}
				admin, err := sql.Open("sqlite", "file:"+files[0])
				if err != nil {
					t.Fatal(err)
				}
				defer admin.Close()
				deadline := time.Now().Add(15 * time.Second)
				for {
					var count int
					err := admin.QueryRow("SELECT COUNT(*) FROM pragma_table_info('state') WHERE name='tag'").Scan(&count)
					if err == nil && count == 1 {
						break
					}
					if time.Now().After(deadline) {
						t.Fatalf("schema watcher did not migrate existing Workspace: %v\n%s", err, output.String())
					}
					time.Sleep(100 * time.Millisecond)
				}
				var preserved int
				if err := admin.QueryRow("SELECT COUNT(*) FROM state WHERE tag='new'").Scan(&preserved); err != nil || preserved != 2 {
					t.Fatalf("migration lost records: %d %v", preserved, err)
				}
			}

		})
	}
}

type localDatabaseTestOutput struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (output *localDatabaseTestOutput) Write(value []byte) (int, error) {
	output.mu.Lock()
	defer output.mu.Unlock()
	return output.buffer.Write(value)
}

func (output *localDatabaseTestOutput) String() string {
	output.mu.Lock()
	defer output.mu.Unlock()
	return output.buffer.String()
}

func TestNativeSandboxProcessExitRevokesDatabaseAccess(t *testing.T) {
	if _, err := exec.LookPath("bun"); err != nil {
		t.Skip("native sandbox requires Bun")
	}
	directory := t.TempDir()
	module := filepath.Join(directory, "session-runtime.mjs")
	for name, source := range map[string]string{"session-runtime.mjs": "await new Promise(() => {});", "local-workspace.mjs": ""} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(source), 0600); err != nil {
			t.Fatal(err)
		}
	}
	databases, _, _ := testLocalDatabases(t, t.TempDir())
	factory, err := newNativeLocalSandboxFactory(module, nil, t.TempDir(), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	factory.databases = databases
	factory.waitReady = func(context.Context, *http.Client, string) error { return nil }
	sandbox, err := factory.Start(context.Background(), localTestWorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sandbox.Close(context.Background()) })
	native := sandbox.(*nativeLocalSandbox)
	if err := native.process.command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-native.process.done:
	case <-time.After(5 * time.Second):
		t.Fatal("native process did not exit")
	}
	databases.mu.Lock()
	count := len(databases.sessions)
	databases.mu.Unlock()
	if count != 0 {
		t.Fatalf("exited process retained %d credential capabilities", count)
	}
}
