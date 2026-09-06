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

test-race:
    mise run test-race

test-integration:
    mise run test-integration

test-integration-race:
    mise run test-integration-race

test-cost:
    mise run test-cost

test-cost-integration:
    mise run test-cost-integration

cover:
    mise run cover

cover-integration:
    mise run cover-integration

bench:
    mise run bench

test-cache-status:
    mise run test-cache-status

test-clean:
    mise run test-clean

golden-update:
    mise run golden-update

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
