package releaseinfo

import "testing"

func TestString(t *testing.T) {
	oldRevision, oldCommit := revision, commit
	t.Cleanup(func() { revision, commit = oldRevision, oldCommit })
	revision, commit = "deploy-20260813-1", "abcdef123456"
	if got := String("fire-fuse"); got != "fire-fuse revision=deploy-20260813-1 commit=abcdef123456" {
		t.Fatalf("String() = %q", got)
	}
}
