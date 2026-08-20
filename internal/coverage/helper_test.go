// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package coverage_test

import "errors"

// errUnrelated stands in for a failure from somewhere else entirely, so that
// the error helpers can be asked about one.
var errUnrelated = errors.New("something else went wrong")
