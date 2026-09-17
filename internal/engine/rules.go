// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package engine

import (
	"strconv"

	"github.com/P4suta/go-mutants/internal/config"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/operatorselect"
)

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

func OperatorRules(registry *mutation.Registry, name string) ([]mutation.Rule, bool) {
	return operatorselect.Resolve(registry, name)
}
