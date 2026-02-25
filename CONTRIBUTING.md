# Contributing to mautrix-mattermost

Thank you for your interest in contributing. This guide will help you get started.

## Prerequisites

- Go 1.26 or later
- Docker and Docker Compose (for integration tests)
- [golangci-lint](https://golangci-lint.run/welcome/install/)

## Development Setup

```bash
# Fork and clone the repository
git clone https://github.com/<your-username>/mautrix-mattermost.git
cd mautrix-mattermost

# Build
make build

# Run tests
make test

# Run linter
make lint
```

## Code Style

- Format code with `gofmt` (enforced by CI)
- Run `go vet` before committing
- Follow [Effective Go](https://go.dev/doc/effective_go) guidelines
- Use table-driven tests where appropriate
- Keep functions focused and small

## Commit Messages

We use [Conventional Commits](https://www.conventionalcommits.org/):

```
<type>(<scope>): <description>

[optional body]

[optional footer(s)]
```

Types: `feat`, `fix`, `docs`, `style`, `refactor`, `test`, `chore`, `ci`

Examples:
```
feat(puppet): add hot-reload API endpoint
fix(connector): handle nil channel in portal creation
docs: update puppet configuration section
test(matterformat): add table-driven tests for HTML conversion
```

## Pull Request Process

1. **Fork** the repository and create a feature branch from `main`.
2. **Implement** your changes with tests.
3. **Run** the full test suite: `make test-race`
4. **Run** the linter: `make lint`
5. **Commit** using conventional commit format.
6. **Push** to your fork and open a Pull Request against `main`.
7. Fill out the PR template completely.
8. **Wait** for CI to pass and a maintainer to review.

### PR Checklist

- [ ] Tests pass (`make test-race`)
- [ ] Linter passes (`make lint`)
- [ ] New code has test coverage
- [ ] Documentation updated if needed
- [ ] No breaking changes (or clearly documented in PR description)
- [ ] Commit messages follow conventional format

## Testing

All new code should include tests.

- **Unit tests**: Place in the same package with `_test.go` suffix
- **Table-driven tests**: Preferred for functions with multiple input/output cases
- **Integration tests**: Use build tags if they require external services

```bash
# Run unit tests
make test

# Run with race detector
make test-race
```

## Issue Labels

| Label | Description |
|-------|-------------|
| `bug` | Something isn't working |
| `enhancement` | New feature or improvement |
| `documentation` | Documentation changes |
| `good first issue` | Good for newcomers |
| `help wanted` | Looking for contributors |
| `question` | Further information is requested |
| `wontfix` | This will not be worked on |

## Code of Conduct

Please be respectful and constructive in all interactions. We expect all participants to treat each other with dignity and professionalism.

## Questions?

Open a [discussion](https://github.com/aiku/mautrix-mattermost/discussions) or ask in a relevant issue.
