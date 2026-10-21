package resourceid

import (
	"strings"
	"testing"
)

func TestPrefixedIdentifiers(t *testing.T) {
	hex := "0123456789abcdef0123456789abcdef"
	for _, test := range []struct {
		name   string
		valid  func(string) bool
		prefix string
	}{
		{"app", ValidAppID, "app_"},
		{"workspace", ValidWorkspaceID, "wsp_"},
		{"sandbox", ValidSandboxID, "sbx-"},
		{"request", ValidRequestID, "req_"},
		{"message", ValidMessageID, "msg_"},
		{"attempt", ValidAttemptID, "att_"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if !test.valid(test.prefix + hex) {
				t.Fatalf("rejected %s", test.prefix+hex)
			}
			for _, invalid := range []string{
				"", test.prefix, test.prefix + hex[:31], test.prefix + hex + "0",
				test.prefix + strings.ToUpper(hex), test.prefix + hex[:31] + "g",
				"xyz_" + hex, " " + test.prefix + hex, test.prefix + hex + "\n",
			} {
				if test.valid(invalid) {
					t.Fatalf("accepted %q", invalid)
				}
			}
		})
	}
}

func TestSessionID(t *testing.T) {
	for _, valid := range []string{"ses_a", "ses_0123456789abcdef0123456789abcdef", "ses_A-b_c", "ses_" + strings.Repeat("a", 60)} {
		if !ValidSessionID(valid) {
			t.Fatalf("rejected %q", valid)
		}
	}
	for _, invalid := range []string{"", "ses_", "ses_-a", "ses_a.b", "ses_" + strings.Repeat("a", 61), "sbx-a"} {
		if ValidSessionID(invalid) {
			t.Fatalf("accepted %q", invalid)
		}
	}
}

func TestSlug(t *testing.T) {
	for _, valid := range []string{"a", "acme", "acme-prod", "a1", strings.Repeat("a", 63)} {
		if !ValidSlug(valid) {
			t.Fatalf("rejected %q", valid)
		}
	}
	for _, invalid := range []string{"", "-a", "a-", "a--b", "Acme", "a_b", "a.b", strings.Repeat("a", 64)} {
		if ValidSlug(invalid) {
			t.Fatalf("accepted %q", invalid)
		}
	}
}
