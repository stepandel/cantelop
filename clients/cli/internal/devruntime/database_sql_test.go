package devruntime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLocalDatabaseSQLFileBoundary(t *testing.T) {
	root := t.TempDir()
	d, err := newLocalDatabases(root, "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	const own, other = "wsp_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "wsp_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	otherDB, err := d.database(other)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := otherDB.Exec("CREATE TABLE private (value TEXT); INSERT INTO private VALUES ('secret')"); err != nil {
		t.Fatal(err)
	}
	ownDB, err := d.database(own)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	conn, err := localDatabaseConn(ctx, ownDB)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	stream := &localDatabaseStream{conn: conn, workspace: own, sql: map[int]string{}}
	checkDenied := func(request localDatabaseRequest) {
		t.Helper()
		if _, err := stream.request(ctx, request); err == nil {
			t.Fatalf("unsafe SQL accepted: %+v", request)
		}
	}
	otherPath := filepath.Join(root, other+".sqlite")
	outside := filepath.Join(root, "outside.sqlite")
	text := "ATTACH DATABASE ? AS sibling"
	checkDenied(localDatabaseRequest{Type: "execute", Stmt: localDatabaseStatement{SQL: &text, Args: []localDatabaseValue{{Type: "text", Value: json.RawMessage(`"` + otherPath + `"`)}}}})
	attach := "ATTACH '" + otherPath + "' AS sibling"
	checkDenied(localDatabaseRequest{Type: "sequence", SQL: &attach})
	vacuum := "VACUUM INTO '" + outside + "'"
	id := 7
	if _, err := stream.request(ctx, localDatabaseRequest{Type: "store_sql", SQLID: &id, SQL: &vacuum}); err != nil {
		t.Fatal(err)
	}
	checkDenied(localDatabaseRequest{Type: "sequence", SQLID: &id})
	var batch localDatabaseRequest
	if err := json.Unmarshal([]byte(`{"type":"batch","batch":{"steps":[{"stmt":{"sql":"ATTACH '`+otherPath+`' AS sibling"}},{"stmt":{"sql_id":7}}]}}`), &batch); err != nil {
		t.Fatal(err)
	}
	response, err := stream.request(ctx, batch)
	if err != nil {
		t.Fatal(err)
	}
	for i, stepErr := range response["result"].(map[string]any)["step_errors"].([]any) {
		if stepErr == nil {
			t.Fatalf("unsafe batch step %d accepted", i)
		}
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Fatalf("VACUUM created an outside file: %v", err)
	}
	var count int
	if err := otherDB.QueryRow("SELECT count(*) FROM private").Scan(&count); err != nil || count != 1 {
		t.Fatalf("sibling database changed: count=%d err=%v", count, err)
	}
	pragma := "PRAGMA \"TEMP_STORE_DIRECTORY\" = '" + root + "'"
	checkDenied(localDatabaseRequest{Type: "sequence", SQL: &pragma})
	checkDenied(localDatabaseRequest{Type: "store_sql", SQLID: &id, SQL: &pragma})
	allowed := "CREATE TABLE ordinary (attach TEXT, vacuum TEXT, attaché TEXT); INSERT INTO ordinary VALUES ('ATTACH', 'VACUUM', 'x') -- ATTACH\r DETACH"
	if _, err := stream.request(ctx, localDatabaseRequest{Type: "sequence", SQL: &allowed}); err != nil {
		t.Fatalf("ordinary SQL rejected: %v", err)
	}
	var named localDatabaseStatement
	if err := json.Unmarshal([]byte(`{"sql":"SELECT attach, :attach, @vacuum FROM ordinary","named_args":[{"name":"attach","value":{"type":"integer","value":"1"}},{"name":"vacuum","value":{"type":"integer","value":"2"}}],"want_rows":true}`), &named); err != nil {
		t.Fatal(err)
	}
	if _, err := stream.request(ctx, localDatabaseRequest{Type: "execute", Stmt: named}); err != nil {
		t.Fatalf("ordinary identifiers and named parameters rejected: %v", err)
	}
}
