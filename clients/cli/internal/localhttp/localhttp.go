// Package localhttp writes the JSON responses of the local development runtime.
package localhttp

import (
	"encoding/json"
	"net/http"
)

func WriteJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}

func WriteError(response http.ResponseWriter, status int, code string) {
	WriteJSON(response, status, map[string]any{"error": map[string]string{"code": code}})
}
