## Summary

Describe what this pull request changes and why it is needed.

## Verification

- [ ] `go test ./... -count=1`
- [ ] `go vet ./...`
- [ ] `golangci-lint run ./...`

## Checklist

- [ ] No manual edits to generated `*.gen.go` files
- [ ] Updated `CHANGELOG.md` (`## [Unreleased]`) for user-visible SDK, CLI, skill, or documentation changes
- [ ] Updated `README.md`, `docs/zh-CN/README.md`, `example_test.go`, or embedded skill docs if public signatures or CLI flags changed
- [ ] Rebased on the latest `main` with passing CI
- [ ] I understand that accepted PRs are integrated into the development repository and closed on GitHub once merged, with full contributor attribution in `CHANGELOG.md` and the next GitHub Release

## Release Attribution

List the GitHub username(s) (e.g. `@your-handle`) to credit in `CHANGELOG.md` and the release notes.
