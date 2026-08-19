// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build windows

package snapshot

import (
	"strings"
	"testing"
)

// TestExtendedPath pins the string transformation itself. The end-to-end deep
// tree test cannot do this job: the Go standard library rewrites long absolute
// paths on its own, so a deep tree would copy correctly even if this helper
// returned its argument unchanged.
func TestExtendedPath(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("d", 30)
	deep := `C:\base\` + strings.Repeat(long+`\`, 9) + "leaf.go"
	if len(deep) < longPathThreshold {
		t.Fatalf("fixture path is only %d characters, shorten the threshold or lengthen the fixture", len(deep))
	}

	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "short path is left alone",
			in:   `C:\a\b\c.go`,
			want: `C:\a\b\c.go`,
		},
		{
			name: "long absolute path is prefixed",
			in:   deep,
			want: `\\?\` + deep,
		},
		{
			name: "forward slashes are converted before prefixing",
			// Under \\?\ nothing is normalized, so a surviving forward slash
			// would become part of a file name and the path would not resolve.
			in:   strings.ReplaceAll(deep, `\`, "/"),
			want: `\\?\` + deep,
		},
		{
			name: "dot elements are resolved before prefixing",
			in:   `C:\base\.\` + strings.Repeat(long+`\`, 9) + "leaf.go",
			want: `\\?\` + deep,
		},
		{
			name: "UNC path takes the UNC spelling",
			in:   `\\server\share\` + strings.Repeat(long+`\`, 9) + "leaf.go",
			want: `\\?\UNC\server\share\` + strings.Repeat(long+`\`, 9) + "leaf.go",
		},
		{
			name: "an already extended path is not prefixed twice",
			in:   `\\?\` + deep,
			want: `\\?\` + deep,
		},
		{
			name: "a device path is left alone",
			in:   `\\.\` + deep,
			want: `\\.\` + deep,
		},
		{
			name: "a long relative path has no extended form",
			in:   strings.Repeat(long+`\`, 9) + "leaf.go",
			want: strings.Repeat(long+`\`, 9) + "leaf.go",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := extendedPath(tt.in); got != tt.want {
				t.Errorf("extendedPath(%q) =\n %q\nwant %q", tt.in, got, tt.want)
			}
		})
	}
}
