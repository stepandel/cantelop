package syncprotocol

import "testing"

func TestValidPathReservedCaseAliases(t *testing.T) {
	for _, p := range []string{
		".GIT/config",
		"src/.GiT/HEAD",
		".CANTELOP/state",
		"Node_Modules/package.json",
		".ENV",
		"src/.Env.local",
		".CANTELOP-SYNC-pending",
	} {
		if ValidPath(p) {
			t.Errorf("accepted reserved case alias %q", p)
		}
	}
	for _, p := range []string{"src/README.md", ".gitignore", ".envrc", "src/myGit/file"} {
		if !ValidPath(p) {
			t.Errorf("rejected ordinary path %q", p)
		}
	}
}
