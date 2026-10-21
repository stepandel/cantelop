package devruntime

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/stepandel/cantelop/clients/cli/internal/ids"
	"github.com/stepandel/cantelop/clients/cli/internal/localhttp"
	"modernc.org/sqlite"
)

const localDatabaseStreamTimeout = time.Minute

const localDatabaseMaxStreams = 128

type localDatabaseGrant struct {
	workspace string
	expires   time.Time
}

type localDatabaseStream struct {
	conn      *sql.Conn
	workspace string
	used      time.Time
	busy      bool
	sql       map[int]string
}

// A stream owns a connection across HTTP requests, preserving transactions.
// Rotating batons serialize each stream; independent streams run concurrently.
type localDatabases struct {
	mu                          sync.Mutex
	root, origin, sandboxOrigin string
	databases                   map[string]*sql.DB
	grants                      map[string]localDatabaseGrant
	sessions                    map[string]string
	streams                     map[string]*localDatabaseStream
	initialize                  func(string) error
	closed                      bool
	active                      sync.WaitGroup
	closeOnce                   sync.Once
	stop                        chan struct{}
	done                        chan struct{}
}

func newLocalDatabases(root, origin, sandboxOrigin string) (*localDatabases, error) {
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() {
		return nil, errors.New("local database root must be a real directory")
	}
	d := &localDatabases{root: root, origin: origin, sandboxOrigin: sandboxOrigin, databases: map[string]*sql.DB{}, grants: map[string]localDatabaseGrant{}, sessions: map[string]string{}, streams: map[string]*localDatabaseStream{}, stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(d.done)
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-d.stop:
				return
			case <-ticker.C:
				d.mu.Lock()
				d.expire(time.Now())
				d.mu.Unlock()
			}
		}
	}()
	return d, nil
}

func localDatabaseToken() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

func (d *localDatabases) expire(now time.Time) {
	for baton, stream := range d.streams {
		if !stream.busy && now.Sub(stream.used) >= localDatabaseStreamTimeout {
			d.closeStream(baton)
		}
	}
	for token, grant := range d.grants {
		if !grant.expires.After(now) {
			delete(d.grants, token)
		}
	}
}

func (d *localDatabases) closeStream(baton string) {
	if stream := d.streams[baton]; stream != nil {
		_, _ = stream.conn.ExecContext(context.Background(), "ROLLBACK")
		_ = stream.conn.Close()
		delete(d.streams, baton)
	}
}

func (d *localDatabases) Close() {
	d.closeOnce.Do(func() {
		d.mu.Lock()
		d.closed = true
		d.mu.Unlock()
		close(d.stop)
		<-d.done
		d.active.Wait()
		d.mu.Lock()
		defer d.mu.Unlock()
		for baton := range d.streams {
			d.closeStream(baton)
		}
		for _, db := range d.databases {
			_ = db.Close()
		}
	})
}

func (d *localDatabases) credentials(workspace, origin string) map[string]string {
	token := localDatabaseToken()
	expires := time.Now().Add(15 * time.Minute)
	d.grants[token] = localDatabaseGrant{workspace, expires}
	return map[string]string{"url": origin + "/databases/" + workspace + "/", "authToken": token, "expiresAt": expires.UTC().Format(time.RFC3339Nano)}
}

func (d *localDatabases) apiCredentials(workspace string) (map[string]string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil, errors.New("local databases closed")
	}
	if _, err := d.database(workspace); err != nil {
		return nil, err
	}
	d.expire(time.Now())
	return d.credentials(workspace, d.origin), nil
}

func (d *localDatabases) sessionEnvironment(workspace string) (map[string]string, func()) {
	d.mu.Lock()
	defer d.mu.Unlock()
	token := localDatabaseToken()
	d.sessions[token] = workspace
	return map[string]string{"CANTELOP_LOCAL_DATABASE_ORIGIN": d.sandboxOrigin, "CANTELOP_WORKSPACE_DATABASE_CREDENTIALS_URL": d.sandboxOrigin + "/internal/v1/runtime/database/credentials", "CANTELOP_WORKSPACE_DATABASE_ACCESS_TOKEN": token}, func() { d.mu.Lock(); delete(d.sessions, token); d.mu.Unlock() }
}

func (d *localDatabases) database(workspace string) (*sql.DB, error) {
	if !ids.ValidLocalWorkspaceID(workspace) {
		return nil, errors.New("invalid Workspace ID")
	}
	if db := d.databases[workspace]; db != nil {
		return db, nil
	}
	path := filepath.Join(d.root, workspace+".sqlite")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err == nil {
		_ = file.Close()
	} else if !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("local database must be a regular file")
	}
	if d.initialize != nil {
		if err := d.initialize(path); err != nil {
			return nil, err
		}
	}
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: path, RawQuery: "_pragma=busy_timeout%285000%29&_pragma=foreign_keys%281%29&_pragma=journal_mode%28WAL%29"}).String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(localDatabaseMaxStreams)
	d.databases[workspace] = db
	return db, nil
}

func (d *localDatabases) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost || r.URL.RawQuery != "" {
		localhttp.WriteError(w, 404, "runtime_route_not_found")
		return
	}
	// No browser credentials or cross-origin requests are accepted.
	if r.Header.Get("Origin") != "" {
		localhttp.WriteError(w, 403, "invalid_origin")
		return
	}
	authorization := r.Header.Get("Authorization")
	if !strings.HasPrefix(authorization, "Bearer ") {
		localhttp.WriteError(w, 401, "database_unauthorized")
		return
	}
	token := strings.TrimPrefix(authorization, "Bearer ")
	d.mu.Lock()
	locked := true
	defer func() {
		if locked {
			d.mu.Unlock()
		}
	}()
	if d.closed {
		localhttp.WriteError(w, 503, "database_unavailable")
		return
	}
	d.expire(time.Now())
	if r.URL.Path == "/internal/v1/runtime/database/credentials" {
		workspace := d.sessions[token]
		if workspace == "" {
			localhttp.WriteError(w, 401, "runtime_unauthorized")
			return
		}
		if _, err := d.database(workspace); err != nil {
			localhttp.WriteError(w, 503, "database_unavailable")
			return
		}
		localhttp.WriteJSON(w, 200, d.credentials(workspace, d.sandboxOrigin))
		return
	}
	grant, ok := d.grants[token]
	if !ok || !grant.expires.After(time.Now()) {
		localhttp.WriteError(w, 401, "database_unauthorized")
		return
	}
	if r.URL.Path != "/databases/"+grant.workspace+"/v2/pipeline" {
		localhttp.WriteError(w, 404, "runtime_route_not_found")
		return
	}
	var input struct {
		Baton    *string                `json:"baton"`
		Requests []localDatabaseRequest `json:"requests"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<20)
	decoder := json.NewDecoder(r.Body)
	if decoder.Decode(&input) != nil || len(input.Requests) > 1024 {
		localhttp.WriteError(w, 400, "invalid_request")
		return
	}
	var baton string
	var stream *localDatabaseStream
	if input.Baton != nil {
		baton = *input.Baton
		stream = d.streams[baton]
		if stream == nil || stream.busy || stream.workspace != grant.workspace {
			localhttp.WriteError(w, 400, "invalid_baton")
			return
		}
		delete(d.streams, baton)
	} else {
		if len(d.streams) >= localDatabaseMaxStreams {
			localhttp.WriteError(w, 503, "database_busy")
			return
		}
		db, err := d.database(grant.workspace)
		if err != nil {
			localhttp.WriteError(w, 503, "database_unavailable")
			return
		}
		conn, err := localDatabaseConn(r.Context(), db)
		if err != nil {
			localhttp.WriteError(w, 503, "database_unavailable")
			return
		}
		stream = &localDatabaseStream{conn: conn, workspace: grant.workspace, sql: map[int]string{}}
	}
	baton = localDatabaseToken()
	stream.used = time.Now()
	stream.busy = true
	d.streams[baton] = stream
	d.active.Add(1)
	d.mu.Unlock()
	locked = false
	defer d.active.Done()
	results := make([]any, 0, len(input.Requests))
	closed := false
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	for _, request := range input.Requests {
		if closed {
			results = append(results, localDatabaseError(errors.New("stream closed")))
			continue
		}
		response, err := stream.request(ctx, request)
		if err != nil {
			results = append(results, localDatabaseError(err))
		} else {
			results = append(results, map[string]any{"type": "ok", "response": response})
		}
		if request.Type == "close" {
			closed = true
		}
	}
	d.mu.Lock()
	locked = true
	stream.busy = false
	stream.used = time.Now()
	var next any = baton
	if closed || ctx.Err() != nil || d.closed {
		d.closeStream(baton)
		next = nil
	}
	d.mu.Unlock()
	locked = false
	localhttp.WriteJSON(w, 200, map[string]any{"baton": next, "base_url": nil, "results": results})
}

type localDatabaseValue struct {
	Type   string          `json:"type"`
	Value  json.RawMessage `json:"value"`
	Base64 string          `json:"base64"`
}

type localDatabaseStatement struct {
	SQL       *string              `json:"sql"`
	SQLID     *int                 `json:"sql_id"`
	Args      []localDatabaseValue `json:"args"`
	NamedArgs []struct {
		Name  string             `json:"name"`
		Value localDatabaseValue `json:"value"`
	} `json:"named_args"`
	WantRows bool `json:"want_rows"`
}

type localDatabaseCondition struct {
	Type  string                   `json:"type"`
	Step  int                      `json:"step"`
	Cond  *localDatabaseCondition  `json:"cond"`
	Conds []localDatabaseCondition `json:"conds"`
}

type localDatabaseBatch struct {
	Steps []struct {
		Stmt      localDatabaseStatement  `json:"stmt"`
		Condition *localDatabaseCondition `json:"condition"`
	} `json:"steps"`
}

type localDatabaseRequest struct {
	Type  string                 `json:"type"`
	Stmt  localDatabaseStatement `json:"stmt"`
	Batch localDatabaseBatch     `json:"batch"`
	SQL   *string                `json:"sql"`
	SQLID *int                   `json:"sql_id"`
}

func localDatabaseError(err error) map[string]any {
	code := "SQLITE_ERROR"
	var se *sqlite.Error
	if errors.As(err, &se) {
		switch se.Code() & 255 {
		case 19:
			code = "SQLITE_CONSTRAINT"
		case 5:
			code = "SQLITE_BUSY"
		case 6:
			code = "SQLITE_LOCKED"
		}
	}
	return map[string]any{"type": "error", "error": map[string]string{"message": err.Error(), "code": code}}
}

func (s *localDatabaseStream) text(text *string, id *int) (string, error) {
	if text != nil && id == nil {
		return *text, rejectLocalDatabaseFileSQL(*text)
	}
	if text == nil && id != nil {
		if text, ok := s.sql[*id]; ok {
			return text, nil
		}
	}
	return "", errors.New("invalid SQL reference")
}

func (s *localDatabaseStream) request(ctx context.Context, r localDatabaseRequest) (map[string]any, error) {
	response := map[string]any{"type": r.Type}
	switch r.Type {
	case "close":
	case "store_sql":
		if r.SQLID == nil || r.SQL == nil || len(s.sql) >= 128 {
			return nil, errors.New("invalid SQL cache entry")
		}
		if err := rejectLocalDatabaseFileSQL(*r.SQL); err != nil {
			return nil, err
		}
		s.sql[*r.SQLID] = *r.SQL
	case "close_sql":
		if r.SQLID == nil {
			return nil, errors.New("invalid SQL cache entry")
		}
		delete(s.sql, *r.SQLID)
	case "execute":
		result, err := s.execute(ctx, r.Stmt)
		if err != nil {
			return nil, err
		}
		response["result"] = result
	case "sequence":
		text, err := s.text(r.SQL, r.SQLID)
		if err != nil {
			return nil, err
		}
		if _, err = s.conn.ExecContext(ctx, text); err != nil {
			return nil, err
		}
	case "batch":
		if len(r.Batch.Steps) > 1024 {
			return nil, errors.New("batch too large")
		}
		results := make([]any, len(r.Batch.Steps))
		errorsOut := make([]any, len(r.Batch.Steps))
		for i, step := range r.Batch.Steps {
			allowed, err := localDatabaseConditionOK(step.Condition, results, errorsOut, i)
			if err != nil {
				return nil, err
			}
			if !allowed {
				continue
			}
			result, err := s.execute(ctx, step.Stmt)
			if err != nil {
				errorsOut[i] = localDatabaseError(err)["error"]
			} else {
				results[i] = result
			}
		}
		response["result"] = map[string]any{"step_results": results, "step_errors": errorsOut}
	default:
		return nil, errors.New("unsupported local database request")
	}
	return response, nil
}

func localDatabaseConditionOK(c *localDatabaseCondition, results, errs []any, current int) (bool, error) {
	if c == nil {
		return true, nil
	}
	switch c.Type {
	case "ok", "error":
		if c.Step < 0 || c.Step >= current {
			return false, errors.New("invalid batch step")
		}
		if c.Type == "ok" {
			return results[c.Step] != nil, nil
		}
		return errs[c.Step] != nil, nil
	case "not":
		if c.Cond == nil {
			return false, errors.New("invalid condition")
		}
		ok, err := localDatabaseConditionOK(c.Cond, results, errs, current)
		return !ok, err
	case "and", "or":
		result := c.Type == "and"
		for _, child := range c.Conds {
			ok, err := localDatabaseConditionOK(&child, results, errs, current)
			if err != nil {
				return false, err
			}
			if c.Type == "and" {
				result = result && ok
			} else {
				result = result || ok
			}
		}
		return result, nil
	}
	return false, errors.New("unsupported batch condition")
}

func (v localDatabaseValue) argument() (any, error) {
	switch v.Type {
	case "null":
		return nil, nil
	case "integer":
		var text string
		if json.Unmarshal(v.Value, &text) != nil {
			return nil, errors.New("invalid integer")
		}
		return strconv.ParseInt(text, 10, 64)
	case "float":
		var number float64
		err := json.Unmarshal(v.Value, &number)
		return number, err
	case "text":
		var text string
		err := json.Unmarshal(v.Value, &text)
		return text, err
	case "blob":
		return base64.StdEncoding.DecodeString(v.Base64)
	}
	return nil, errors.New("invalid argument type")
}

func (s *localDatabaseStream) execute(ctx context.Context, stmt localDatabaseStatement) (map[string]any, error) {
	text, err := s.text(stmt.SQL, stmt.SQLID)
	if err != nil {
		return nil, err
	}
	args := make([]any, 0, len(stmt.Args)+len(stmt.NamedArgs))
	for _, value := range stmt.Args {
		arg, err := value.argument()
		if err != nil {
			return nil, err
		}
		args = append(args, arg)
	}
	for _, value := range stmt.NamedArgs {
		arg, err := value.Value.argument()
		if err != nil {
			return nil, err
		}
		args = append(args, sql.Named(strings.TrimLeft(value.Name, ":@$"), arg))
	}
	var before int64
	if err = s.conn.QueryRowContext(ctx, "SELECT total_changes()").Scan(&before); err != nil {
		return nil, err
	}
	rows, err := s.conn.QueryContext(ctx, text, args...)
	if err != nil {
		return nil, err
	}
	names, err := rows.Columns()
	if err != nil {
		_ = rows.Close()
		return nil, err
	}
	types, _ := rows.ColumnTypes()
	cols := make([]any, len(names))
	for i, name := range names {
		decl := ""
		if i < len(types) {
			decl = types[i].DatabaseTypeName()
		}
		cols[i] = map[string]string{"name": name, "decltype": decl}
	}
	out := make([]any, 0)
	for rows.Next() {
		values := make([]any, len(names))
		targets := make([]any, len(names))
		for i := range targets {
			targets[i] = &values[i]
		}
		if err = rows.Scan(targets...); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if stmt.WantRows {
			row := make([]any, len(values))
			for i, value := range values {
				switch v := value.(type) {
				case nil:
					row[i] = map[string]string{"type": "null"}
				case int64:
					row[i] = map[string]string{"type": "integer", "value": strconv.FormatInt(v, 10)}
				case float64:
					row[i] = map[string]any{"type": "float", "value": v}
				case string:
					row[i] = map[string]string{"type": "text", "value": v}
				case []byte:
					row[i] = map[string]string{"type": "blob", "base64": base64.StdEncoding.EncodeToString(v)}
				default:
					_ = rows.Close()
					return nil, fmt.Errorf("unsupported SQLite value %T", value)
				}
			}
			out = append(out, row)
			if len(out) > 100000 {
				_ = rows.Close()
				return nil, errors.New("local result exceeds 100000 rows")
			}
		}
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	var after, changed, rowid int64
	if err = s.conn.QueryRowContext(ctx, "SELECT total_changes(), changes(), last_insert_rowid()").Scan(&after, &changed, &rowid); err != nil {
		return nil, err
	}
	if before == after {
		changed = 0
	}
	return map[string]any{"cols": cols, "rows": out, "affected_row_count": changed, "last_insert_rowid": strconv.FormatInt(rowid, 10)}, nil
}
