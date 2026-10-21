package devruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stepandel/cantelop/clients/cli/internal/projectconfig"
)

func TestSDKOneSchemaWatch(t *testing.T) {
	sdk := os.Getenv("CANTELOP_SDK_DIRECTORY")
	if sdk == "" {
		t.Skip("set CANTELOP_SDK_DIRECTORY to qualify the real SDK")
	}
	for _, existing := range []bool{true, false} {
		name := "existing-schema"
		if !existing {
			name = "new-schema"
		}
		t.Run(name, func(t *testing.T) {
			root, output := t.TempDir(), t.TempDir()
			write := func(name, contents string) {
				t.Helper()
				path := filepath.Join(root, name)
				if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(contents), 0644); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.MkdirAll(filepath.Join(root, "node_modules", "@cantelop"), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(sdk, filepath.Join(root, "node_modules", "@cantelop", "sdk")); err != nil {
				t.Fatal(err)
			}
			write("package.json", `{"type":"module"}`)
			definition := `import {CantelopClient} from "@cantelop/sdk";const client=new CantelopClient();export const agent=client.app({name:"watch-test",runtime:{receive(){}}});`
			write("cantelop.ts", definition)
			write("cantelop.json", `{"schema_version":3,"definition":"cantelop.ts"}`)
			writeSchema := func() {
				write("db/columns.ts", `import {text} from "@cantelop/sdk/schema";export const columns={value:text()};`)
				write("db/schema.ts", `import {sqliteTable} from "@cantelop/sdk/schema";import {columns} from "./columns.js";export const state=sqliteTable("state",columns);`)
			}
			if existing {
				writeSchema()
			}
			config, err := projectconfig.Load(filepath.Join(root, "cantelop.json"))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			watcher, err := startLocalBuildWatcher(ctx, config, filepath.Join(output, "api"), filepath.Join(output, "session-runtime"), "http://127.0.0.1:8080", io.Discard)
			if err != nil {
				t.Fatal(err)
			}
			defer watcher.Close()
			manifestPath := filepath.Join(output, "session-runtime", "cantelop-runtime.json")
			readManifest := func() []byte {
				t.Helper()
				value, err := os.ReadFile(manifestPath)
				if err != nil {
					t.Fatal(err)
				}
				return value
			}
			// watch() performs the first build itself; there must be no second startup build.
			select {
			case event := <-watcher.events:
				t.Fatalf("unexpected startup rebuild: %#v", event)
			case <-time.After(500 * time.Millisecond):
			}
			awaitRuntimeBuild := func(previous []byte) []byte {
				t.Helper()
				deadline := time.After(10 * time.Second)
				for {
					select {
					case event := <-watcher.events:
						if event.Error != "" {
							t.Fatal(event.Error)
						}
						if event.Component == "session-runtime" {
							next := readManifest()
							if bytes.Equal(previous, next) {
								t.Fatal("runtime rebuild retained stale manifest")
							}
							return next
						}
					case <-watcher.done:
						t.Fatalf("watcher exited: %v", watcher.WaitError())
					case <-deadline:
						t.Fatal("schema edit did not rebuild runtime manifest")
					}
				}
			}
			before := readManifest()
			if !existing {
				writeSchema()
				before = awaitRuntimeBuild(before)
				if !bytes.Contains(before, []byte(`"database_schema"`)) {
					t.Fatal("new schema missing from manifest")
				}
			}
			// Neither this dependency nor schema.ts is imported by the App definition.
			write("db/columns.ts", `import {text} from "@cantelop/sdk/schema";export const columns={value:text(),extra:text()};`)
			after := awaitRuntimeBuild(before)
			if !bytes.Contains(after, []byte(`"extra"`)) {
				t.Fatal("schema dependency edit was not published")
			}
			// Runtime changes still publish matching Edge/runtime/backend identities.
			write("cantelop.ts", `import {CantelopClient} from "@cantelop/sdk";const client=new CantelopClient();export const agent=client.app({name:"watch-test",runtime:{receive(){console.log("updated");}}});`)
			awaitRuntimeBuild(after)
			edge, err := projectconfig.LoadAPIArtifact(filepath.Join(output, "api"))
			if err != nil {
				t.Fatal(err)
			}
			var runtime projectconfig.SessionRuntimeManifest
			if err := json.Unmarshal(readManifest(), &runtime); err != nil {
				t.Fatal(err)
			}
			document, err := os.ReadFile(filepath.Join(root, ".cantelop/client/cantelop-client.json"))
			if err != nil {
				t.Fatal(err)
			}
			var backend struct {
				Apps []struct {
					RuntimeID string `json:"session_runtime_id"`
				} `json:"apps"`
			}
			if err := json.Unmarshal(document, &backend); err != nil {
				t.Fatal(err)
			}
			if len(backend.Apps) != 1 || backend.Apps[0].RuntimeID != runtime.SessionRuntimeID || edge.Manifest.SessionRuntimeID != runtime.SessionRuntimeID || runtime.SessionRuntimeID == config.RuntimeID {
				t.Fatal("rebuild artifacts have inconsistent runtime identities")
			}

			if existing {
				lastGood := readManifest()
				write("cantelop.ts", "export const agent = ;")
				select {
				case event := <-watcher.events:
					if event.Error == "" {
						t.Fatal("invalid definition was published")
					}
				case <-time.After(10 * time.Second):
					t.Fatal("invalid definition was not reported")
				}
				if !bytes.Equal(lastGood, readManifest()) {
					t.Fatal("invalid edit replaced the last good artifacts")
				}
				// Drain the second component's error before checking recovery.
				select {
				case event := <-watcher.events:
					if event.Error == "" {
						t.Fatal("missing runtime build error")
					}
				case <-time.After(time.Second):
					t.Fatal("missing runtime build error")
				}
				write("cantelop.ts", definition)
				awaitRuntimeBuild(lastGood)
			}
		})
	}
}
