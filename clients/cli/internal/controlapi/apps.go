package controlapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/stepandel/cantelop/clients/cli/internal/ids"
)

type App struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
}

type ConfigValue struct {
	Name  string  `json:"name"`
	Kind  string  `json:"kind"`
	Value *string `json:"value,omitempty"`
}

type ConfigValuePage struct {
	Items []ConfigValue `json:"items"`
}

func ResolveAppID(ctx context.Context, session *Session, slug string) (string, error) {
	if !ids.ValidRoutingSlug(slug) {
		return "", errors.New("App slug is invalid")
	}
	var app App
	if err := session.Request(ctx, http.MethodGet, "/v1/apps/by-slug/"+slug, "", nil, &app); err != nil {
		return "", fmt.Errorf("resolve App %q: %w", slug, err)
	}
	if !ids.ValidAppID(app.ID) || app.Slug != slug {
		return "", errors.New("control plane returned a different App")
	}
	return app.ID, nil
}

func CreateApp(ctx context.Context, session *Session, slug string) (App, error) {
	if !ids.ValidRoutingSlug(slug) {
		return App{}, errors.New("App slug is invalid")
	}
	input := map[string]any{
		"slug": slug,
		"session_runtime": map[string]any{
			"runtime": map[string]any{"profile": "default"},
		},
	}
	document, err := json.Marshal(input)
	if err != nil {
		return App{}, err
	}
	var app App
	if err := session.Request(ctx, http.MethodPost, "/v1/apps", "application/json", bytes.NewReader(document), &app); err != nil {
		return App{}, err
	}
	if !ids.ValidAppID(app.ID) || app.Slug != slug {
		return App{}, errors.New("control plane returned a different App")
	}
	return app, nil
}
