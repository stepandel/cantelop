package nfs

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// WorkspaceFilesystem identifies the running, data-only JuiceFS mount. It is
// emitted by the pinned dependency patch only with --workspace-data-only.
const WorkspaceFilesystem = "fuse.juicefs-workspace-v1"

var ErrWorkspacePolicy = errors.New("workspace mount does not enforce the required data-only policy")

// CheckWorkspaceMount checks the kernel's mount table, not a marker file on
// customer-controlled storage or the version of an installed-but-unused binary.
// Mount administration and the running host kernel remain trusted.
func CheckWorkspaceMount(root string) error {
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return fmt.Errorf("%w: %v", ErrWorkspacePolicy, err)
	}
	defer f.Close()
	return checkWorkspaceMount(root, f)
}

func checkWorkspaceMount(root string, input io.Reader) error {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	matches := 0
	unescape := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 10 {
			return fmt.Errorf("%w: malformed mount table", ErrWorkspacePolicy)
		}
		if unescape.Replace(fields[4]) != root {
			continue
		}
		matches++
		sep := -1
		for i := 6; i < len(fields); i++ {
			if fields[i] == "-" {
				sep = i
				break
			}
		}
		if sep < 0 || len(fields) != sep+4 {
			return fmt.Errorf("%w: malformed mount entry", ErrWorkspacePolicy)
		}
		if unescape.Replace(fields[3]) != "/" || fields[sep+1] != WorkspaceFilesystem {
			return fmt.Errorf("%w: require a full %s mount at %s", ErrWorkspacePolicy, WorkspaceFilesystem, root)
		}
		options := "," + fields[sep+3] + ","
		for _, required := range []string{"default_permissions", "allow_other", "user_id=0"} {
			if !strings.Contains(options, ","+required+",") {
				return fmt.Errorf("%w: missing %s", ErrWorkspacePolicy, required)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("%w: %v", ErrWorkspacePolicy, err)
	}
	if matches != 1 {
		return fmt.Errorf("%w: require exactly one mount at %s, got %d", ErrWorkspacePolicy, root, matches)
	}
	return nil
}
