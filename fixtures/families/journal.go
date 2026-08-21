// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package families

import "strconv"

// A journal collects the lines Record writes into it.
type journal struct {
	lines []string
}

// add appends one line to the journal.
//
// KILLED. The assignment is `delete-assignment`, `append` removal included: the
// statement disappears, the slice never grows, and a journal that records
// nothing is what the test sees. It is a plain `=` rather than a `:=` on
// purpose — deletion never removes a declaration, because every later use of
// the name would stop compiling.
func (j *journal) add(line string) {
	j.lines = append(j.lines, line)
}

// Record writes every value into a journal and returns what it wrote.
//
// KILLED. The call is `delete-call-statement`, the third rule of the deletion
// family, and the slice that comes back is `return-nil` — the ordinary nillable
// replacement rather than the error-swallowing one, because its type is not
// `error`. Nothing here calls `panic`, which is the one call the deletion family
// declines: removing a terminating panic manufactures a missing return instead
// of a mutant.
func Record(values []int) []string {
	j := &journal{}
	for _, v := range values {
		j.add(strconv.Itoa(v))
	}
	return j.lines
}
