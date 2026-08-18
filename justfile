# SPDX-FileCopyrightText: 2026 go-mutants contributors
# SPDX-License-Identifier: MIT OR Apache-2.0

# A thin shim over mise. Every gate is defined exactly once, in mise.toml, so
# that `just`, `mise run`, the git hooks, and CI can never drift apart.

set windows-shell := ["pwsh", "-NoLogo", "-NoProfile", "-Command"]

default:
    @just --list

bootstrap:
    mise run bootstrap

build:
    mise run build

test:
    mise run test

test-integration:
    mise run test-integration

fmt:
    mise run fmt

lint:
    mise run lint

check:
    mise run check

dogfood:
    mise run dogfood

package:
    mise run package

hooks:
    mise run hooks
