package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/stepandel/cantelop/fire-fuse/controlclient"
	"github.com/stepandel/cantelop/fire-fuse/edgeprotocol"
	"github.com/stepandel/cantelop/internal/resourceid"
)

var environmentPath = regexp.MustCompile(`^/__cantelop/v1/environment/(set|unset|status)$`)
var environmentName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

type EnvironmentRequest struct {
	Type     string  `json:"type"`
	Name     string  `json:"name,omitempty"`
	Value    *string `json:"value,omitempty"`
	Secret   *bool   `json:"secret,omitempty"`
	Revision *int64  `json:"revision,omitempty"`
}
type EnvironmentUpdate struct {
	Revision int64  `json:"revision"`
	Status   string `json:"status"`
}
type EnvironmentProvider interface {
	UpdateEnvironment(context.Context, string, string, EnvironmentRequest) (EnvironmentUpdate, error)
}

func (c *ProductClient) UpdateEnvironment(ctx context.Context, appID, action string, input EnvironmentRequest) (EnvironmentUpdate, error) {
	if !resourceid.ValidAppID(appID) || !validEnvironmentInput(action, input) {
		return EnvironmentUpdate{}, &ControlError{StatusCode: http.StatusBadRequest}
	}
	document, err := json.Marshal(input)
	if err != nil {
		return EnvironmentUpdate{}, err
	}
	var result EnvironmentUpdate
	err = controlclient.Do(ctx, c.client, c.baseURL, http.MethodPost, "/internal/v1/apps/"+appID+"/environment/"+action, bytes.NewReader(document), &result)
	if err != nil {
		return EnvironmentUpdate{}, err
	}
	if result.Revision < 0 || (result.Status != "pending" && result.Status != "applied" && result.Status != "failed") || (action == "status" && result.Revision != *input.Revision) {
		return EnvironmentUpdate{}, errors.New("invalid environment update response")
	}
	return result, nil
}

func validEnvironmentInput(action string, input EnvironmentRequest) bool {
	if input.Type != "app.environment."+action {
		return false
	}
	if action == "status" {
		return input.Revision != nil && *input.Revision >= 0 && input.Name == "" && input.Secret == nil && input.Value == nil
	}
	if (action != "set" && action != "unset") || !environmentName.MatchString(input.Name) || strings.HasPrefix(input.Name, "CANTELOP_") || input.Secret == nil || input.Revision != nil {
		return false
	}
	if action == "unset" {
		return input.Value == nil
	}
	return input.Value != nil && len(*input.Value) <= 64*1024 && !strings.ContainsRune(*input.Value, '\x00')
}

func (h *Handler) serveEnvironment(w http.ResponseWriter, r *http.Request, target edgeprotocol.DispatchTarget) bool {
	match := environmentPath.FindStringSubmatch(r.URL.Path)
	if match == nil {
		return false
	}
	w.Header().Set("Cache-Control", "private, no-store")
	if r.Method != http.MethodPost || r.URL.RawQuery != "" {
		w.Header().Set("Allow", http.MethodPost)
		writeError(w, http.StatusMethodNotAllowed, "runtime_method_not_allowed", "environment updates require POST without a query")
		return true
	}
	var input EnvironmentRequest
	if decodeEdgeJSON(w, r, &input, 6*64*1024+1024) != nil || !validEnvironmentInput(match[1], input) {
		writeError(w, http.StatusBadRequest, "invalid_environment_request", "invalid environment request")
		return true
	}
	if h.environment == nil {
		writeError(w, http.StatusServiceUnavailable, "environment_unavailable", "environment control unavailable")
		return true
	}
	result, err := h.environment.UpdateEnvironment(r.Context(), target.AppID, match[1], input)
	if err != nil {
		var failure *ControlError
		// Quota exhaustion is permanent until values are removed, so callers must not retry it as an outage.
		if errors.As(err, &failure) && (failure.StatusCode == http.StatusBadRequest || failure.StatusCode == http.StatusNotFound || failure.StatusCode == http.StatusConflict || failure.StatusCode == http.StatusTooManyRequests) {
			writeError(w, failure.StatusCode, "environment_update_rejected", "environment update rejected")
		} else {
			writeError(w, http.StatusServiceUnavailable, "environment_unavailable", "environment control unavailable")
		}
		return true
	}
	writeEdgeJSON(w, http.StatusOK, result)
	return true
}
