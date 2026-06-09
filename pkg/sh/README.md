# pkg/sh — forked shell runtime

This package tree is a heavily modified fork of [`mvdan/sh`](https://github.com/mvdan/sh),
the POSIX shell parser and interpreter. `CHANGELOG.md` in this directory is the
upstream project's changelog and documents the fork point's lineage; swarmd's own
changes are described in this repository's commit history and pull requests.

## What is upstream vs swarmd-specific

Upstream packages (track `mvdan/sh`; avoid local changes beyond what the fork
requires):

- `syntax/` — shell parser, AST, printer
- `interp/` — interpreter core (see exceptions below)
- `expand/`, `pattern/`, `fileutil/`, `shell/` — expansion, globbing, helpers
- `cmd/` — gosh / shfmt entry points

swarmd-specific additions (owned by this repository, covered by its lint gates):

- `sandbox/` — root-constrained filesystem policy and the sandboxed runner
- `memfs/` — in-memory implementation of the sandbox filesystem contracts
- `moreinterp/coreutils/` — in-process coreutils (ls, sed, grep, tar, ...)
  so agent shells never execute host binaries
- `interp/network.go`, `interp/http.go`, `interp/host_matcher.go`,
  `interp/filesystem.go` — the network-policy and filesystem seams the
  sandbox builds on

## Update policy

Upstream files intentionally keep upstream style and may carry findings that
swarmd's static analysis would reject; CI lint gates therefore cover only the
swarmd-specific paths listed above. A few `interp` tests compare against the
host `bash` and are environment-sensitive (see `#IGNORE` markers in the test
tables).
