package main

import "testing"

import (
	"net/http"
	"strings"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/controlauth"
)

func TestDefaultEdgeTokenPathIsPrivateGatewayConfiguration(t *testing.T) {
	if defaultEdgeTokenPath != "/etc/fire-fuse-gateway/edge-ingress.token" {
		t.Fatalf("defaultEdgeTokenPath = %q", defaultEdgeTokenPath)
	}
}

func TestDefaultScopedTokenPaths(t *testing.T) {
	if defaultRuntimeTokenPath != "/etc/fire-fuse-gateway/runtime-control.token" || defaultProductTokenPath != "/etc/fire-fuse-gateway/product-control.token" {
		t.Fatal("gateway credential paths are not service-private")
	}
}

func TestAuthenticatedClientsKeepRuntimeAndProductCredentialsSeparate(t *testing.T) {
	runtimeToken, productToken, edgeToken := strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)
	runtimeClient, productClient, err := authenticatedClients(time.Second, runtimeToken, productToken, edgeToken)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		client *http.Client
		token  string
	}{{runtimeClient, runtimeToken}, {productClient, productToken}} {
		transport, ok := tc.client.Transport.(controlauth.Transport)
		if !ok || transport.Token != tc.token {
			t.Fatal("client credential crossed authority boundary")
		}
	}
	for name, tokens := range map[string][]string{
		"runtime_product": {runtimeToken, runtimeToken, edgeToken},
		"runtime_edge":    {runtimeToken, productToken, runtimeToken},
		"product_edge":    {runtimeToken, productToken, productToken},
	} {
		if _, _, err := authenticatedClients(time.Second, tokens[0], tokens[1], tokens[2]); err == nil {
			t.Fatalf("%s shared credential accepted", name)
		}
	}
}

func TestValidateListenAddress(t *testing.T) {
	for _, address := range []string{"127.0.0.1:8081", "[::1]:8081"} {
		if err := validateListenAddress(address); err != nil {
			t.Errorf("validateListenAddress(%q) error = %v", address, err)
		}
	}
	for _, address := range []string{"0.0.0.0:8081", "192.0.2.1:8081", "localhost:8081", "127.0.0.1:0"} {
		if err := validateListenAddress(address); err == nil {
			t.Errorf("validateListenAddress(%q) error = nil", address)
		}
	}
}
