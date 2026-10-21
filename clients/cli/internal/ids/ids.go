// Package ids holds the identifier formats the CLI exchanges with the control
// plane and the local runtime.
package ids

import (
	"regexp"

	"github.com/stepandel/cantelop/internal/resourceid"
)

var (
	DeploymentUploadIDPattern = regexp.MustCompile(`^upl_[0-9a-f]{32}$`)
	ReleaseIDPattern          = regexp.MustCompile(`^rel_[0-9a-f]{32}$`)
	WorkspaceIDPattern        = regexp.MustCompile(`^wsp_[A-Za-z0-9][A-Za-z0-9_-]{0,59}$`)
	DockerImageIDPattern      = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	ConfigNamePattern         = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)
)

var (
	LocalSessionKeyPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	LocalContainerIDPattern = regexp.MustCompile(`^[0-9a-f]{12,64}$`)
)

// The formats shared with the edge, Console, and Fire Fuse come from
// resourceid so the CLI cannot drift from them.
var (
	ValidAppID            = resourceid.ValidAppID
	ValidLocalWorkspaceID = resourceid.ValidWorkspaceID
	ValidSessionID        = resourceid.ValidSessionID
	ValidRoutingSlug      = resourceid.ValidSlug
)
