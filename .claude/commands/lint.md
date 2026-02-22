Run all linting and static analysis checks on the codebase.

1. Run `go vet ./...`
2. Run `gofmt -l .` to check for formatting issues
3. If `golangci-lint` is available, run `golangci-lint run ./...`

Report any issues found, grouped by severity. For each issue, show the file, line, and a brief fix suggestion.
