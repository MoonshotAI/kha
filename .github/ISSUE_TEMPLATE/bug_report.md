---
name: Bug report
about: Report a bug in the KHA Go SDK or CLI
title: "bug: "
labels: ["bug"]
---

## What happened?

A clear and concise description of the problem and what you expected to happen instead.

## Environment

- `kha version` output (CLI and pinned API version):
- Go version (`go version`, if using the SDK):
- OS and architecture (e.g. `darwin/arm64`, `linux/amd64`, `windows/amd64`):

## Steps to Reproduce

Provide a minimal CLI command sequence or self-contained Go snippet that reproduces the issue:

```go
// Minimal Go reproduction or shell commands
```

## Actual Output / Error Log

Paste the full error message, exit code, or unexpected output here. If the issue involves an API request, please include the `Msh-Request-Id` response header if available.

> **Security reminder:** Please redact any API Keys (`sk-...`), work keys, tokens, or sensitive payload contents before submitting this issue.
