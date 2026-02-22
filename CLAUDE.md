# CLAUDE.md — mautrix-mattermost

## Project Overview

A Matrix-Mattermost bridge built on the mautrix bridgev2 framework with first-class puppet identity routing. Each Matrix user can post to Mattermost under a dedicated bot identity rather than a generic relay account.

## Language & Build

- **Language**: Go 1.24 (go.mod); CI tests 1.22, 1.23
- **Module**: `github.com/aiku/mautrix-mattermost`
- **Build**: `make build` (preferred — includes version ldflags) or `go build ./cmd/mautrix-mattermost/`
- **Run**: `./mautrix-mattermost -c config.yaml`
- **Test**: `go test ./... -v` or `make test`
- **Test + race**: `go test -race ./... -v` or `make test-race`
- **Lint**: `golangci-lint run ./...` or `make lint`
- **Format**: `gofmt -s -w .` or `make fmt`
- **Vet**: `go vet ./...` or `make vet`

## Project Structure

```
cmd/mautrix-mattermost/   # Main entry point
pkg/connector/             # Core bridge connector
  connector.go             # MattermostConnector, puppet loading, hot-reload
  client.go                # MattermostClient, WebSocket, channel sync
  handlematrix.go          # Matrix → Mattermost message handling
  handlemattermost.go      # Mattermost → Matrix event handling
  chatinfo.go              # Channel/user info conversion
  ids.go                   # Network ID mapping helpers
  login.go                 # Token + password login flows
  config.go                # Configuration + display name template
  formatting.go            # Format delegation
pkg/connector/matrixfmt/   # Matrix HTML → Mattermost markdown
pkg/connector/mattermostfmt/ # Mattermost markdown → Matrix HTML
```

## Key Types

- `MattermostConnector` — core connector implementing bridgev2.NetworkConnector
- `MattermostClient` — authenticated MM user connection (WebSocket + REST)
- `PuppetClient` — per-user bot client (MXID → MM bot mapping)
- `PuppetEntry` — JSON-serializable puppet config (`Slug`, `MXID`, `Token` — all strings; defined in `pkg/connector/connector.go`)

## Critical Patterns

### Puppet System
- Env var pattern: `MATTERMOST_PUPPET_{SLUG}_MXID` + `MATTERMOST_PUPPET_{SLUG}_TOKEN`
- Slug: uppercase, hyphens → underscores (e.g., `my-bot` → `MY_BOT`)
- Example:
  ```
  MATTERMOST_PUPPET_ALICE_MXID=@alice:example.com
  MATTERMOST_PUPPET_ALICE_TOKEN=bot-token-for-alice
  MATTERMOST_PUPPET_BOB_SMITH_MXID=@bob-smith:example.com
  MATTERMOST_PUPPET_BOB_SMITH_TOKEN=bot-token-for-bob
  ```
- `resolvePostClient()`: origSender → evt.Sender → relay fallback (3-path resolution)
- **Never hardcode bot prefixes** — use `Config.BotPrefix`

### Echo Prevention
Multi-layer, critical for preventing infinite loops:
1. Puppet bot user ID check (`IsPuppetUserID`)
2. Bridge bot user ID check
3. Relay bot user ID check
4. Configurable username prefix check (`isBridgeUsername`)
5. System message filtering
**Never simplify or remove echo prevention layers.**

### Hot-Reload
- `POST /api/reload-puppets` — no body reloads from env; with JSON body:
  ```json
  {"puppets": [{"slug": "ALICE", "mxid": "@alice:example.com", "token": "bot-token"}]}
  ```
- `WatchNewPortals()` — continuous goroutine for new portal rooms
- Both are essential for dynamic bot provisioning at runtime

## Testing Standards

Full testing standards are defined in `.claude/agents/test-writer.md`. Use the `test-writer` agent for writing new tests. Key principles:

- **Behavior over coverage** — every test must assert a meaningful behavioral property. Never write tests just to touch lines of code.
- **Negative & boundary testing required** — zero values, nil inputs, malformed data, wrong types, error paths with specific error checks (`errors.Is`), concurrency under `-race`.
- **Fuzz testing required** for parsing, encoding/decoding, validation, and byte manipulation functions.
- **Don't match tests to code** — think independently about correct behavior. If the code is wrong, document with `// BUG:` comments.
- **Table-driven tests** with `[]struct{ name string; ... }` for multiple input classes.
- **No test frameworks** — stdlib `testing` package only.
- Use `zerolog.Nop()` for test loggers, `newFakeMM()` for API simulation, `mockEventSender` for event capture.
- Test files colocated: `foo.go` → `foo_test.go`
- Run with `-race` flag: `make test-race`

## Security

Full security review standards are defined in `.claude/agents/security-reviewer.md`. Use the `security-reviewer` agent or `/security-review` command for security audits. Key rules:

- **STRIDE threat model** required for every PR — assess all 6 categories (Spoofing, Tampering, Repudiation, Info Disclosure, DoS, Elevation of Privilege).
- **OWASP Top 10:2025 mapping** for every finding — use the shared language (A01-A10).
- **govulncheck** on every PR that touches `go.mod`/`go.sum` — blocking if call stack reaches vulnerable code.
- **gosec** findings need explicit disposition — fix, suppress with `//nolint:gosec // reason`, or accept with documented risk.
- **No secrets in code or logs** — puppet tokens, API keys, and credentials must never appear in source, logs, or error messages.
- **`crypto/rand`** mandatory for security-sensitive randomness — `math/rand` (v1) is banned.
- **Constant-time comparison** (`crypto/subtle.ConstantTimeCompare`) for all secret comparisons.
- **Dependency vetting** — new deps checked for license, maintenance status, transitive blast radius, typosquatting.
- **Race detector** (`go test -race`) failures are blocking — no exceptions.
- **Fuzz tests** required for all parsing, validation, and encoding/decoding functions.

### Project-Specific Security Surfaces
- **Echo prevention** — 5 layers, never simplify or remove (prevents infinite bridge loops)
- **Admin API** (`POST /api/reload-puppets`) — must validate input, bound request size, log actions
- **WebSocket event parsing** — untrusted input from Mattermost, validated via `parsePostedEvent` etc.
- **Puppet token handling** — tokens loaded from env vars, never logged or serialized into errors
- **Format conversion** — user-generated content goes through `matrixfmt/`/`mattermostfmt/`, must handle malicious input

## Dependencies

Target latest versions; run `go list -m -versions <module> | tr ' ' '\n' | tail -1` to check.

- `maunium.net/go/mautrix` — Matrix bridge framework (bridgev2). Current: v0.23.3, latest: v0.26.3
- `github.com/mattermost/mattermost/server/public` — MM API client. Current: v0.1.12, latest: v0.2.0
- `github.com/rs/zerolog` — Structured logging. Current: v1.34.0 (latest)
- `go.mau.fi/util` — mautrix utilities. Current: v0.8.6, latest: v0.9.6

## Rules

- All new features need tests
- Run `gofmt -s -w .` and `go vet ./...` before committing (both enforced by CI)
- Use [Conventional Commits](https://www.conventionalcommits.org/): `feat(scope):`, `fix(scope):`, `docs:`, `test:`, `refactor:`, `chore:`, `ci:`
- Keep echo prevention intact — it prevents infinite loops
- Puppet resolution order matters: origSender > evt.Sender > relay
- Use structured zerolog logging (not fmt.Printf or log.Println)
- This is an open-source project — no internal/proprietary references
