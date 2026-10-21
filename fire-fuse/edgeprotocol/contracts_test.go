package edgeprotocol

import "testing"

const (
	testAppID     = "app_0123456789abcdef0123456789abcdef"
	testRequestID = "req_0123456789abcdef0123456789abcdef"
	testAppSlug   = "base-agent"
	testHostname  = "base-agent.cantelop.dev"
)

func TestPublicAppHostnameRoundTripsSlug(t *testing.T) {
	hostname, err := PublicAppHostname(testAppSlug)
	if err != nil || hostname != testHostname {
		t.Fatalf("PublicAppHostname() = %q, %v", hostname, err)
	}
	appSlug, err := AppSlugFromHostname(hostname)
	if err != nil || appSlug != testAppSlug {
		t.Fatalf("AppSlugFromHostname() = %q, %v", appSlug, err)
	}
}

func TestPublicAppHostnameRejectsAliases(t *testing.T) {
	for _, hostname := range []string{
		"base_agent.cantelop.dev",
		"Base-agent.cantelop.dev",
		"workspace--base-agent.cantelop.dev",
		testHostname + ".",
		testHostname + ":443",
		"extra." + testHostname,
		"cantelop.dev",
	} {
		if _, err := AppSlugFromHostname(hostname); err == nil {
			t.Errorf("AppSlugFromHostname(%q) succeeded", hostname)
		}
	}
}

func TestAPIInvocationValidatesIDAndHostnameShapes(t *testing.T) {
	valid := APIInvocation{RequestID: testRequestID, AppID: testAppID, Hostname: testHostname}
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	invalid := valid
	invalid.AppID = "app_invalid"
	if err := invalid.Validate(); err == nil {
		t.Fatal("Validate() accepted a malformed App ID")
	}
	otherApp := valid
	otherApp.AppID = "app_ffffffffffffffffffffffffffffffff"
	if err := otherApp.Validate(); err != nil {
		t.Fatalf("Validate() coupled routed slug to App ID: %v", err)
	}
}

func TestReservedHeadersAreEdgeScoped(t *testing.T) {
	headers := ReservedHeaders()
	want := []string{
		"X-Cantelop-Edge-Dispatch",
		"X-Cantelop-Edge-Authorization",
		"X-Cantelop-Edge-Request-ID",
		"X-Cantelop-Edge-App-ID",
		"X-Cantelop-Edge-Workspace-ID",
		"X-Cantelop-Edge-Hostname",
	}
	if len(headers) != len(want) {
		t.Fatalf("ReservedHeaders() = %#v", headers)
	}
	for i := range want {
		if headers[i] != want[i] {
			t.Fatalf("ReservedHeaders()[%d] = %q, want %q", i, headers[i], want[i])
		}
	}
}
