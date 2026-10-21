package edgeprotocol

import "testing"

func TestOperatorDomainKeepsExactAppBoundary(t *testing.T) {
	domain := "agents.example.net"
	host, err := PublicAppHostname("demo", domain)
	if err != nil || host != "demo.agents.example.net" {
		t.Fatalf("hostname = %q, %v", host, err)
	}
	invocation := APIInvocation{RequestID: "req_0123456789abcdef0123456789abcdef", AppID: "app_0123456789abcdef0123456789abcdef", Hostname: host}
	if err := invocation.Validate(domain); err != nil {
		t.Fatal(err)
	}
	if err := invocation.Validate(); err == nil {
		t.Fatal("operator hostname was accepted by the hosted default")
	}
	for _, value := range []string{"demo.cantelop.dev", "demo.agents.example.net.evil.example", "a.b.agents.example.net", "agents.example.net", "demo.agents.example.net.", "demo.agents.example.net:443", "Demo.agents.example.net"} {
		if _, err := AppSlugFromHostname(value, domain); err == nil {
			t.Errorf("accepted hostname %q", value)
		}
	}
}

func TestInvalidOperatorDomains(t *testing.T) {
	for _, domain := range []string{"", "example.net.", "EXAMPLE.net", "*.example.net", "https://example.net", "example.net:443", "127.0.0.1", "example..net", "example.net/", " example.net"} {
		if err := ValidateAppDomain(domain); err == nil {
			t.Errorf("accepted domain %q", domain)
		}
	}
}
