package main

import (
	"fmt"

	"github.com/stepandel/cantelop/fire-fuse/controlauth"
)

func loadControlTokens(paths map[string]string) (map[string]string, error) {
	tokens := make(map[string]string, len(paths))
	for role, path := range paths {
		token, err := controlauth.LoadTokenFile(path)
		if err != nil {
			return nil, fmt.Errorf("load %s control-plane token: %w", role, err)
		}
		tokens[role] = token
	}
	if err := validateControlTokenSet(tokens); err != nil {
		return nil, err
	}
	return tokens, nil
}

func validateControlTokenSet(tokens map[string]string) error {
	return controlauth.ValidateDistinctTokens(
		tokens["operator"], tokens["gateway"], tokens["dispatcher"],
		tokens["observer"], tokens["compute"],
	)
}

func controlCredentials(operatorToken, gatewayToken, dispatcherToken, observerToken string) ([]controlauth.Credential, error) {
	operator, err := controlauth.NewScope("/")
	if err != nil {
		return nil, err
	}
	gateway, err := controlauth.NewScope(
		"GET /v1/apps/{app_id}/workspace-sync/{workspace_id}",
		"PUT /v1/apps/{app_id}/workspace-sync/{workspace_id}",
		"DELETE /v1/apps/{app_id}/workspace-sync/{workspace_id}",
		"GET /v1/apps",
		"GET /v1/apps/{app_id}/workspaces/by-slug/{workspace_slug}",
		"GET /v1/workspaces", "GET /v1/workspaces/{id}",
		"POST /v1/sessions", "GET /v1/sessions/{id}", "GET /v1/sessions/{id}/route", "DELETE /v1/sessions/{id}",
		"POST /v1/message-deliveries", "GET /v1/message-deliveries/{id}", "GET /v1/message-deliveries/{id}/wait",
		"GET /v1/session-events", "GET /v1/session-view",
	)
	if err != nil {
		return nil, err
	}
	dispatcher, err := controlauth.NewScope(
		"GET /v1/apps", "GET /v1/workspaces", "GET /v1/workspaces/{id}",
		"POST /v1/sessions", "GET /v1/sessions/{id}", "GET /v1/sessions/{id}/route",
		"POST /v1/pinned-work-leases", "GET /v1/message-executions", "POST /v1/message-executions",
		"GET /v1/message-delivery-notifications", "POST /v1/message-deliveries/claim", "POST /v1/message-deliveries/requeue-handling",
		"GET /v1/message-deliveries/{id}",
		"POST /v1/message-deliveries/{id}/requeue",
		"POST /v1/message-deliveries/{id}/reply", "POST /v1/message-deliveries/{id}/fail",
		"DELETE /v1/sandboxes/{id}", "POST /v1/session-events",
	)
	if err != nil {
		return nil, err
	}
	observer, err := controlauth.NewScope(
		"GET /v1/apps", "GET /v1/apps/{id}", "GET /v1/workspaces", "GET /v1/sessions", "GET /v1/sandboxes", "GET /v1/sandboxes/{id}",
		"GET /v1/sandbox-usage", "GET /v1/host-capacity", "GET /v1/request-events", "GET /v1/message-delivery-events",
		"DELETE /v1/sessions/{id}", "DELETE /v1/sandboxes/{id}", "POST /v1/sandboxes/{id}/crash",
	)
	if err != nil {
		return nil, err
	}
	return []controlauth.Credential{
		{Token: operatorToken, Scope: operator}, {Token: gatewayToken, Scope: gateway},
		{Token: dispatcherToken, Scope: dispatcher}, {Token: observerToken, Scope: observer},
	}, nil
}
