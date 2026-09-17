// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package operatorselect

import "github.com/P4suta/go-mutants/internal/mutation"

func Select(tier mutation.Tier, names []string) (rules []mutation.Rule, unknown string) {
	registry := mutation.CanonicalRegistry()
	if len(names) == 0 {
		return registry.SelectTier(tier), ""
	}
	selected := make(map[string]bool)
	for _, name := range names {
		named, ok := Resolve(registry, name)
		if !ok {
			return nil, name
		}
		for _, rule := range named {
			selected[rule.Name] = true
		}
	}
	rules = make([]mutation.Rule, 0, len(selected))
	for _, rule := range registry.Rules() {
		if selected[rule.Name] {
			rules = append(rules, rule)
		}
	}
	return rules, ""
}

func Resolve(registry *mutation.Registry, name string) ([]mutation.Rule, bool) {
	if _, ok := registry.FamilyPosition(mutation.Family(name)); ok {
		return registry.FamilyRules(mutation.Family(name)), true
	}
	if rule, ok := registry.Lookup(name); ok {
		return []mutation.Rule{rule}, true
	}
	return nil, false
}
