<!--
SPDX-FileCopyrightText: 2026 go-mutants contributors
SPDX-License-Identifier: MIT OR Apache-2.0
-->

# Pull request

## Summary

<!-- What changed, and why? -->

## User and compatibility impact

<!-- Note CLI, config, report-schema, stable-ID, or migration implications.
     Rule names and versions participate in mutant identity: changing what a
     rule emits changes every ID it mints. -->

## Validation

- [ ] `mise run check`
- [ ] Relevant integration tests (`mise run test-integration`)
- [ ] New behavior has a regression test

## Release and privacy

- [ ] No generated mutation report, source-bearing diagnostic, or secret is included
- [ ] Public contract changes include documentation and schema fixtures
- [ ] Release notes are updated when user-visible behavior changes
