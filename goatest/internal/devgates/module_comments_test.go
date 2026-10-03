// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package devgates_test

import (
	"fmt"
	"strings"
	"testing"

	"golang.org/x/mod/modfile"
)

func moduleCommentOffenders(data []byte) ([]string, error) {
	file, err := modfile.Parse("go.mod", data, nil)
	if err != nil {
		return nil, err
	}
	protected := make(map[int]bool)
	protect := func(comments *modfile.Comments) {
		for _, group := range [][]modfile.Comment{comments.Before, comments.Suffix} {
			for _, comment := range group {
				protected[comment.Start.Byte] = true
			}
		}
	}
	for _, retraction := range file.Retract {
		if retraction.Rationale == "" {
			continue
		}
		comments := retraction.Syntax.Comment()
		if len(comments.Before)+len(comments.Suffix) == 0 {
			for _, statement := range file.Syntax.Stmt {
				block, ok := statement.(*modfile.LineBlock)
				if !ok {
					continue
				}
				for _, line := range block.Line {
					if line == retraction.Syntax {
						comments = block.Comment()
					}
				}
			}
		}
		protect(comments)
	}
	for _, dependency := range file.Require {
		if dependency.Indirect {
			for _, comment := range dependency.Syntax.Suffix {
				if strings.TrimSpace(strings.TrimPrefix(comment.Token, "//")) == "indirect" {
					protected[comment.Start.Byte] = true
				}
			}
		}
	}
	if file.Module != nil && file.Module.Deprecated != "" {
		comments := file.Module.Syntax.Comment()
		matched := false
		var paragraph []modfile.Comment
		matchParagraph := func() {
			var lines []string
			for _, comment := range paragraph {
				lines = append(lines, strings.TrimSpace(strings.TrimPrefix(comment.Token, "//")))
			}
			text := strings.Join(lines, "\n")
			message, deprecated := strings.CutPrefix(text, "Deprecated:")
			if !matched && deprecated && strings.TrimLeft(message, " ") == file.Module.Deprecated {
				for _, comment := range paragraph {
					protected[comment.Start.Byte] = true
				}
				matched = true
			}
			paragraph = nil
		}
		for _, group := range [][]modfile.Comment{comments.Before, comments.Suffix} {
			for _, comment := range group {
				if !strings.HasPrefix(comment.Token, "//") {
					continue
				}
				if strings.TrimSpace(strings.TrimPrefix(comment.Token, "//")) == "" {
					matchParagraph()
				} else {
					paragraph = append(paragraph, comment)
				}
			}
		}
		matchParagraph()
	}
	var offenders []string
	check := func(comments *modfile.Comments) {
		for _, group := range [][]modfile.Comment{comments.Before, comments.Suffix, comments.After} {
			for _, comment := range group {
				body := strings.TrimSpace(strings.TrimPrefix(comment.Token, "//"))
				if body == "" || protected[comment.Start.Byte] || strings.HasPrefix(body, "SPDX-") {
					continue
				}
				offenders = append(offenders, fmt.Sprintf("line %d", comment.Start.Line))
			}
		}
	}
	check(file.Syntax.Comment())
	for _, statement := range file.Syntax.Stmt {
		check(statement.Comment())
		switch statement := statement.(type) {
		case *modfile.CommentBlock, *modfile.Line:
		case *modfile.LineBlock:
			check(statement.LParen.Comment())
			check(statement.RParen.Comment())
			for _, line := range statement.Line {
				check(line.Comment())
			}
		default:
			return nil, fmt.Errorf("unsupported module syntax %T", statement)
		}
	}
	return offenders, nil
}

func TestModuleCommentRulesPreserveCompilerObservedMetadata(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		body string
		want int
	}{
		{name: "no comments", body: "module example.com/fixture\ngo 1.26\n"},
		{name: "license", body: "// SPDX-License-Identifier: MIT\nmodule example.com/fixture\n"},
		{name: "retraction reason", body: "module example.com/fixture\nretract v0.1.0 // Broken API.\n"},
		{name: "block retraction reason", body: "module example.com/fixture\n// Broken API.\nretract (\nv0.1.0\n)\n"},
		{name: "indirect dependency", body: "module example.com/fixture\nrequire example.com/dependency v1.0.0 // indirect\n"},
		{name: "deprecation", body: "// Deprecated: Use example.com/replacement.\nmodule example.com/fixture\n"},
		{name: "licensed deprecation", body: "// SPDX-License-Identifier: MIT\n//\n// Deprecated: Use example.com/replacement.\nmodule example.com/fixture\n"},
		{name: "deprecation with observed suffix", body: "// Deprecated: Use example.com/replacement.\nmodule example.com/fixture // It preserves this API.\n"},
		{name: "multiline licensed deprecation", body: "// SPDX-License-Identifier: MIT\n//\n// Deprecated:  Use example.com/replacement.\n// It preserves this API.\nmodule example.com/fixture\n"},
		{name: "deprecation with history", body: "// Unneeded history.\n//\n// Deprecated: Use example.com/replacement.\n//\n// More history.\nmodule example.com/fixture\n", want: 2},
		{name: "nonparagraph deprecation", body: "// SPDX-License-Identifier: MIT\n// Deprecated: Use example.com/replacement.\nmodule example.com/fixture\n", want: 1},
		{name: "unobserved note", body: "// Unneeded history.\nmodule example.com/fixture\n", want: 1},
		{name: "unobserved block history", body: "module example.com/fixture\n// Unneeded history.\nretract (\nv0.1.0 // Broken API.\n)\n", want: 1},
		{name: "trailing note", body: "module example.com/fixture\n// Unneeded history.\n", want: 1},
		{name: "indirect plus prose", body: "module example.com/fixture\nrequire example.com/dependency v1.0.0 // indirect with history\n", want: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			actual, err := moduleCommentOffenders([]byte(test.body))
			if err != nil || len(actual) != test.want {
				t.Fatalf("module comment contract: offenders = %v, error = %v; want %d", actual, err, test.want)
			}
		})
	}
	if _, err := moduleCommentOffenders([]byte("not a module directive")); err == nil {
		t.Fatal("an invalid module is rejected rather than exempted")
	}
}
