Review the current codebase or staged changes for security issues using the security-reviewer agent.

Follow the Go Security Agent prompt in full:

1. Run all security tools in order:
   ```
   go vet ./...
   ```
   ```
   gosec ./...
   ```
   ```
   govulncheck ./...
   ```
   ```
   make test-race
   ```

2. If there are staged or uncommitted changes, run `git diff` and `git diff --cached` to scope the review.

3. Produce the full structured review:
   - Tool results summary
   - STRIDE assessment (all 6 categories with explicit N/A/PASS/FINDING)
   - OWASP Top 10:2025 mapping for every finding
   - Dependency changes audit (if go.mod/go.sum changed)
   - Findings ordered by severity with file:line, CWE, OWASP category, concrete fix
   - Final verdict: APPROVE / REQUEST CHANGES / BLOCKING

4. Pay special attention to project-specific security surfaces:
   - Echo prevention layer integrity (5 layers, never weaken)
   - Puppet token handling (never log tokens)
   - Admin API authentication (`POST /api/reload-puppets`)
   - WebSocket event parsing (untrusted input from Mattermost)
   - Format conversion (user-generated content parsing)

When reviewing or writing security-related code, follow the full standards in `.claude/agents/security-reviewer.md`.

5. **Stamp file**: If the final verdict is **APPROVE**, create the security review stamp by running:
   ```
   touch .claude/security-review-stamp
   ```
   This stamp is checked by the pre-commit hook — commits are blocked until the stamp exists and is newer than all `.go` source files. If the verdict is REQUEST CHANGES or BLOCKING, do NOT create the stamp.
