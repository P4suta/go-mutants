<!-- SPDX-License-Identifier: MIT OR Apache-2.0 -->

# Release assets

Reconcile unpublished GitHub drafts through the authenticated `gh` CLI.
Existing assets must match SHA-256 and size.
Missing assets are uploaded without replacement.
The command never publishes releases.

```console
mise x -- cargo run --locked \
  --manifest-path tools/release-assets/Cargo.toml -- upload-draft --help
```
