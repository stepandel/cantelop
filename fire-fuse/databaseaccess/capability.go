// Package databaseaccess signs capabilities bound to platform sandbox identity.
package databaseaccess

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"

	"github.com/stepandel/cantelop/internal/resourceid"
)

type Identity struct {
	SandboxID   string `json:"sandboxId"`
	AppID       string `json:"appId"`
	WorkspaceID string `json:"workspaceId"`
}
type Access struct {
	endpoint string
	key      []byte
}

func New(endpoint, keyHex string) (*Access, error) {
	key, err := hex.DecodeString(keyHex)
	parsed, urlErr := url.Parse(endpoint)
	if err != nil || len(key) != 32 || keyHex != hex.EncodeToString(key) || urlErr != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "/internal/v1/runtime/database/credentials" {
		return nil, errors.New("invalid workspace database access configuration")
	}
	return &Access{endpoint: endpoint, key: key}, nil
}
func (a *Access) Environment(identity Identity) ([]string, error) {
	if !resourceid.ValidSandboxID(identity.SandboxID) || !resourceid.ValidAppID(identity.AppID) || !resourceid.ValidWorkspaceID(identity.WorkspaceID) {
		return nil, errors.New("invalid database sandbox identity")
	}
	document, _ := json.Marshal(identity)
	encoded := base64.RawURLEncoding.EncodeToString(document)
	mac := hmac.New(sha256.New, a.key)
	mac.Write([]byte("cantelop-workspace-database-v1\x00" + encoded))
	capability := encoded + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return []string{"CANTELOP_WORKSPACE_DATABASE_CREDENTIALS_URL=" + a.endpoint, "CANTELOP_WORKSPACE_DATABASE_ACCESS_TOKEN=" + capability}, nil
}
