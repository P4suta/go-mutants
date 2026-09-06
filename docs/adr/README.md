<!--
SPDX-FileCopyrightText: 2026 go-mutants contributors
SPDX-License-Identifier: MIT OR Apache-2.0
-->

# Architecture decision records

A decision gets a record here when it constrains code that has not been written
yet — when the interesting part is not what was built but what may not be built
on top of it. Everything else belongs in a package doc, beside the code it
explains.

An ADR says what the situation was, what was decided, and what that costs. It is
not revised when the code changes: a record whose consequences no longer hold is
superseded by a new one, so that reading them in order is reading the history of
the design rather than a snapshot of it.

| ADR | Decision |
| --- | --- |
| [0001](0001-trace-is-not-evidence.md) | A trace is the diagnostic account of a run and is never evidence: it takes no part in a verdict, an identity or a cache key, and a recording that cannot be written costs a note rather than the run |

For the contracts these decisions produced, see
[the JSON contracts](../json-schema.md) and
[the trace format](../trace-v1.md); for how the pieces fit together, see
[the architecture](../architecture.md).
