package cmd

import "testing"

func TestFormatVersion(t *testing.T) {
	const sha = "57f653fae6741a1af905edb351b57d7afe318012"
	const when = "2026-10-07T09:35:28Z"

	tests := []struct {
		name                  string
		version, commit, date string
		want                  string
	}{
		{"release", "0.2.0", sha, when, "0.2.0 (57f653f, 2026-10-07)"},
		{"tag from build info", "v0.2.0", sha, when, "0.2.0 (57f653f, 2026-10-07)"},
		{"go install", "v0.2.0", "", "", "0.2.0"},
		{"no date", "0.2.0", sha, "", "0.2.0 (57f653f)"},
		{"untagged build", "v0.2.1-0.20261007093528-57f653fae674+dirty", sha, when, "0.2.1-0.20261007093528-57f653fae674+dirty"},
		{"no build info", "", "", "", "dev"},
		{"devel", "(devel)", "", "", "dev"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatVersion(tt.version, tt.commit, tt.date); got != tt.want {
				t.Errorf("formatVersion(%q, %q, %q) = %q, want %q", tt.version, tt.commit, tt.date, got, tt.want)
			}
		})
	}
}
