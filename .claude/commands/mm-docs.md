Look up Mattermost or mautrix documentation using the mattermost-expert agent.

Use context7 as the primary source with these library IDs:
- `/mattermost/docs` — MM platform docs
- `/websites/developers_mattermost_api-documentation` — REST API reference
- `/mattermost/mattermost` — MM source code and Go types
- `/mautrix/go` — mautrix Go framework and bridgev2
- `/websites/mau_fi_bridges` — Bridge setup and configuration docs

Fall back to firecrawl search against `developers.mattermost.com`, `docs.mau.fi`, or `pkg.go.dev` if context7 doesn't cover it.

After finding the answer, cross-reference with the local codebase in `pkg/connector/` to show how it's currently used in this project.

Answer the user's question with: the answer, the source, local context, and any caveats.
