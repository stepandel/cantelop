package devruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/stepandel/cantelop/clients/cli/internal/projectconfig"
)

// The SDK owns schema generation, SQLite migration SQL, and application migration history.
// CLI code only coordinates when they run and which Workspace file they target.
var sdkDatabaseMigrationProgram = projectconfig.SDKLoadProgram + `
const schema = JSON.parse(process.argv[2]);
// SDKs that predate schema support still run projects that declare no schema.
const sdk = await loadSDK(...(schema === null ? [] : ["synchronizeLocalDatabase"]));
if (typeof sdk.synchronizeLocalDatabase === "function") {
  await sdk.synchronizeLocalDatabase(process.argv[1], schema ?? undefined);
}
`

func configureLocalDatabaseSchema(databases *localDatabases, root, apiModule string) {
	databases.initialize = func(filename string) error {
		manifestPath := filepath.Join(filepath.Dir(apiModule), "cantelop-api.json")
		if filepath.Base(apiModule) == "session-runtime.mjs" {
			manifestPath = filepath.Join(filepath.Dir(apiModule), "cantelop-runtime.json")
		}
		document, err := os.ReadFile(manifestPath)
		if err != nil {
			return err
		}
		var manifest struct {
			DatabaseSchema json.RawMessage `json:"database_schema"`
		}
		if err := json.Unmarshal(document, &manifest); err != nil {
			return err
		}
		schema := string(manifest.DatabaseSchema)
		if schema == "" {
			schema = "null"
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, "node", "--input-type=module", "--eval", sdkDatabaseMigrationProgram, filename, schema)
		command.Dir = root
		output, err := command.CombinedOutput()
		if err != nil {
			return fmt.Errorf("apply Workspace schema: %s", output)
		}
		return nil
	}
}

// refreshSchema migrates without holding d.mu: an open client transaction must
// still be able to reach ServeHTTP and commit while the migration waits.
func (d *localDatabases) refreshSchema() error {
	d.mu.Lock()
	if d.closed || d.initialize == nil {
		d.mu.Unlock()
		return nil
	}
	initialize := d.initialize
	workspaces := make([]string, 0, len(d.databases))
	for workspace := range d.databases {
		workspaces = append(workspaces, workspace)
	}
	d.active.Add(1)
	d.mu.Unlock()
	defer d.active.Done()
	for _, workspace := range workspaces {
		if err := initialize(filepath.Join(d.root, workspace+".sqlite")); err != nil {
			return err
		}
	}
	return nil
}
