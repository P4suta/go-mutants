// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package engine

import (
	"strconv"

	"github.com/P4suta/go-mutants/internal/config"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/operatorselect"
)

// SelectRules resolves a configured selection into the rules discovery runs.
//
// The profile is a tier and selects monotonically; a named operator is looked
// up in the whole registry instead, so a family outside the profile is honoured
// rather than silently dropped. Saying "run the bitwise family" and being given
// nothing because the profile is balanced would be the kind of quiet
// disagreement between two settings that this tool refuses everywhere else.
//
// The result is in canonical registry order whatever order the names were
// written in, because rule order is part of what makes a catalogue
// reproducible.
//
// It lives here rather than in internal/cli because `run` and `list` must
// select identically: a listing that showed a mutant a run would not execute,
// or the other way round, would make `--mutant` unusable. Both call this.
func SelectRules(cfg config.Config) ([]mutation.Rule, error) {
	rules, unknown := operatorselect.Select(cfg.Mutation.Profile, cfg.Mutation.Operators)
	if unknown != "" {
		return nil, &Error{
			Code: CodeUnknownOperator,
			Message: "unknown operator " + strconv.Quote(unknown) +
				": expected an operator family or a rule name from the v1 catalogue",
		}
	}
	return rules, nil
}

// OperatorRules resolves one operator name to the rules it stands for: a family
// name stands for the whole family, a rule name for itself.
//
// It is exported alongside [SelectRules] so that the selection and every
// diagnostic about it read the same catalogue the same way. A warning that has
// to answer "what did *this* name select" would otherwise re-implement the
// lookup, and a warning that describes a selection nobody made is worse than no
// warning.
func OperatorRules(registry *mutation.Registry, name string) ([]mutation.Rule, bool) {
	return operatorselect.Resolve(registry, name)
}
