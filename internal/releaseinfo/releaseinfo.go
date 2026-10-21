// Package releaseinfo exposes build identity embedded into distributable binaries.
package releaseinfo

import "fmt"

var (
	revision = "dev"
	commit   = "unknown"
)

func String(component string) string {
	return fmt.Sprintf("%s revision=%s commit=%s", component, revision, commit)
}
