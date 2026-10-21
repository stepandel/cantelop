package httpapi

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stepandel/cantelop/fire-fuse/runtime/microvm"
)

type computeRegistrarFunc func(string, microvm.RegisterComputeAppSpec) (microvm.App, bool, error)

func (f computeRegistrarFunc) RegisterComputeAppForUser(userID string, spec microvm.RegisterComputeAppSpec) (microvm.App, bool, error) {
	return f(userID, spec)
}

func TestRegisterComputeAppAcceptsExplicitDesiredState(t *testing.T) {
	var gotUser string
	var got microvm.RegisterComputeAppSpec
	handler := registerComputeApp(computeRegistrarFunc(func(userID string, spec microvm.RegisterComputeAppSpec) (microvm.App, bool, error) {
		gotUser, got = userID, spec
		return microvm.App{ID: spec.ID}, true, nil
	}))
	body := []byte(`{"slug":"agent","build_id":"sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef","session_runtime":{"runtime":{"internal_port":3000,"profile":"default"}}}`)
	request := httptest.NewRequest(http.MethodPut, "/v1/internal/users/usr_0123456789abcdef0123456789abcdef/apps/app_0123456789abcdef0123456789abcdef", bytes.NewReader(body))
	request.SetPathValue("user_id", "usr_0123456789abcdef0123456789abcdef")
	request.SetPathValue("app_id", "app_0123456789abcdef0123456789abcdef")
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated || gotUser != "usr_0123456789abcdef0123456789abcdef" ||
		got.ID != "app_0123456789abcdef0123456789abcdef" ||
		got.BuildID != "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" ||
		got.SessionRuntime.Runtime.InternalPort != 3000 {
		t.Fatalf("registration = %d %s, user=%q desired=%#v", recorder.Code, recorder.Body.String(), gotUser, got)
	}
}

func TestRegisterComputeAppRejectsRemovedRuntimePolicy(t *testing.T) {
	handler := registerComputeApp(computeRegistrarFunc(func(string, microvm.RegisterComputeAppSpec) (microvm.App, bool, error) {
		t.Fatal("registrar called for a removed runtime policy")
		return microvm.App{}, false, nil
	}))
	body := []byte(`{"slug":"agent","workspace":{"id":"wsp_0123456789abcdef0123456789abcdef","slug":"production"},"session_runtime":{"runtime":{"internal_port":3000,"profile":"default"},"policy":{"min_instances_per_workspace":0}}}`)
	request := httptest.NewRequest(http.MethodPut, "/v1/internal/users/usr_0123456789abcdef0123456789abcdef/apps/app_0123456789abcdef0123456789abcdef", bytes.NewReader(body))
	request.SetPathValue("user_id", "usr_0123456789abcdef0123456789abcdef")
	request.SetPathValue("app_id", "app_0123456789abcdef0123456789abcdef")
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("registration = %d %s, want bad request", recorder.Code, recorder.Body.String())
	}
}

func TestRegisterComputeWorkspaceAcceptsExplicitDesiredState(t *testing.T) {
	var got microvm.RegisterComputeWorkspaceSpec
	handler := registerComputeWorkspace(computeWorkspaceRegistrarFunc(func(_ string, spec microvm.RegisterComputeWorkspaceSpec) (microvm.Workspace, bool, error) {
		got = spec
		return microvm.Workspace{ID: spec.ID}, true, nil
	}))
	request := httptest.NewRequest(http.MethodPut, "/", bytes.NewBufferString(`{"slug":"staging"}`))
	request.SetPathValue("user_id", "usr_0123456789abcdef0123456789abcdef")
	request.SetPathValue("app_id", "app_0123456789abcdef0123456789abcdef")
	request.SetPathValue("workspace_id", "wsp_abcdef0123456789abcdef0123456789")
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated || got.ID != "wsp_abcdef0123456789abcdef0123456789" ||
		got.AppID != "app_0123456789abcdef0123456789abcdef" {
		t.Fatalf("registration = %d %s, desired=%#v", recorder.Code, recorder.Body.String(), got)
	}
}

type computeWorkspaceRegistrarFunc func(string, microvm.RegisterComputeWorkspaceSpec) (microvm.Workspace, bool, error)

func (f computeWorkspaceRegistrarFunc) RegisterComputeWorkspaceForUser(userID string, spec microvm.RegisterComputeWorkspaceSpec) (microvm.Workspace, bool, error) {
	return f(userID, spec)
}

type computeAppArchiverFunc func(context.Context, string, string) (bool, error)

func (f computeAppArchiverFunc) ArchiveAppForUser(ctx context.Context, userID, appID string) (bool, error) {
	return f(ctx, userID, appID)
}

type computeWorkspaceArchiverFunc func(context.Context, string, string, string) (bool, error)

func (f computeWorkspaceArchiverFunc) ArchiveWorkspaceForUser(ctx context.Context, userID, appID, workspaceID string) (bool, error) {
	return f(ctx, userID, appID, workspaceID)
}

type computeWorkspacePurgerFunc func(context.Context, string, string, string) error

func (f computeWorkspacePurgerFunc) PurgeWorkspaceForUser(ctx context.Context, userID, appID, workspaceID string) error {
	return f(ctx, userID, appID, workspaceID)
}

func TestArchiveComputeWorkspaceTreatsDesiredAbsenceAsSuccess(t *testing.T) {
	handler := archiveComputeWorkspace(computeWorkspaceArchiverFunc(func(context.Context, string, string, string) (bool, error) {
		return false, microvm.ErrWorkspaceNotFound
	}))
	request := httptest.NewRequest(http.MethodDelete, "/", nil)
	request.SetPathValue("user_id", "usr_0123456789abcdef0123456789abcdef")
	request.SetPathValue("app_id", "app_0123456789abcdef0123456789abcdef")
	request.SetPathValue("workspace_id", "wsp_abcdef0123456789abcdef0123456789")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Body.String() != `{"workspace_id":"wsp_abcdef0123456789abcdef0123456789","status":"archived"}`+"\n" {
		t.Fatalf("archive = %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestPurgeComputeWorkspaceReturnsCompletedTombstone(t *testing.T) {
	var got []string
	handler := purgeComputeWorkspace(computeWorkspacePurgerFunc(func(_ context.Context, userID, appID, workspaceID string) error {
		got = []string{userID, appID, workspaceID}
		return nil
	}))
	request := httptest.NewRequest(http.MethodDelete, "/", nil)
	request.SetPathValue("user_id", "usr_0123456789abcdef0123456789abcdef")
	request.SetPathValue("app_id", "app_0123456789abcdef0123456789abcdef")
	request.SetPathValue("workspace_id", "wsp_abcdef0123456789abcdef0123456789")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Body.String() != `{"workspace_id":"wsp_abcdef0123456789abcdef0123456789","status":"purged"}`+"\n" || len(got) != 3 {
		t.Fatalf("purge = %d %s, got=%#v", recorder.Code, recorder.Body.String(), got)
	}
}

func TestArchiveComputeAppTreatsDesiredAbsenceAsSuccess(t *testing.T) {
	handler := archiveComputeApp(computeAppArchiverFunc(func(context.Context, string, string) (bool, error) {
		return false, microvm.ErrAppNotFound
	}))
	request := httptest.NewRequest(http.MethodDelete, "/", nil)
	request.SetPathValue("user_id", "usr_0123456789abcdef0123456789abcdef")
	request.SetPathValue("app_id", "app_0123456789abcdef0123456789abcdef")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Body.String() != `{"app_id":"app_0123456789abcdef0123456789abcdef","status":"archived"}`+"\n" {
		t.Fatalf("archive = %d %s", recorder.Code, recorder.Body.String())
	}
}

type computeConfigProjectorFunc func(string, string, int64, []microvm.ConfigValue) (bool, error)

func (f computeConfigProjectorFunc) ReplaceComputeAppConfigForUser(userID, appID string, revision int64, values []microvm.ConfigValue) (bool, error) {
	return f(userID, appID, revision, values)
}

func TestReplaceComputeConfigAcceptsCompleteRevision(t *testing.T) {
	var gotRevision int64
	var gotValues []microvm.ConfigValue
	handler := replaceComputeConfig(computeConfigProjectorFunc(func(_, _ string, revision int64, values []microvm.ConfigValue) (bool, error) {
		gotRevision, gotValues = revision, values
		return true, nil
	}))
	body := `{"revision":3,"values":[{"name":"LOG_LEVEL","kind":"variable","value":"debug"},{"name":"API_TOKEN","kind":"secret","value":"secret"}]}`
	request := httptest.NewRequest(http.MethodPut, "/", bytes.NewBufferString(body))
	request.SetPathValue("user_id", "usr_0123456789abcdef0123456789abcdef")
	request.SetPathValue("app_id", "app_0123456789abcdef0123456789abcdef")
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || gotRevision != 3 || len(gotValues) != 2 ||
		gotValues[1].Kind != microvm.ConfigValueSecret || gotValues[1].Value != "secret" {
		t.Fatalf("projection = %d %s, revision=%d values=%#v", recorder.Code, recorder.Body.String(), gotRevision, gotValues)
	}
}
