# Configuration

## Config File

The bridge uses a YAML config file for its network-specific settings. The bridgev2 framework handles the overall bridge configuration (database, homeserver connection, appservice registration, logging, etc.) -- the settings below are Mattermost-specific.

The example config is embedded in the binary at build time from `pkg/connector/example-config.yaml`.

### Fields

```yaml
# URL of the Mattermost server to connect to.
# This is the default server URL used for puppet connections and auto-login.
# Individual puppets can override this with their own _URL env var.
# Can also be set via the login flow on a per-user basis.
server_url: "https://mattermost.example.com"

# Displayname template for Mattermost ghost users in Matrix.
# Uses Go text/template syntax.
# Available variables: .Username, .Nickname, .FirstName, .LastName
displayname_template: "{{if .Nickname}}{{.Nickname}}{{else}}{{.Username}}{{end}} (MM)"

# Username prefix for echo prevention. Any Mattermost username starting with
# this prefix is treated as a bridge-managed bot and its posts will not be
# relayed back to Matrix. Leave empty to disable prefix-based filtering.
bot_prefix: ""

# Listen address for the admin HTTP API (serves /api/reload-puppets).
# Set to empty string to disable the admin API.
# Can be overridden via BRIDGE_API_ADDR environment variable.
admin_api_addr: ":29320"
```

### Display Name Template

The `displayname_template` field uses Go's `text/template` syntax. It controls how Mattermost users appear in Matrix rooms.

Available variables (from `DisplaynameParams`):

| Variable | Source | Example |
|----------|--------|---------|
| `.Username` | Mattermost username | `john.doe` |
| `.Nickname` | Mattermost nickname | `Johnny` |
| `.FirstName` | Mattermost first name | `John` |
| `.LastName` | Mattermost last name | `Doe` |

Template examples:

```yaml
# Nickname with username fallback (default)
displayname_template: "{{if .Nickname}}{{.Nickname}}{{else}}{{.Username}}{{end}} (MM)"

# Full name with username fallback
displayname_template: "{{if .FirstName}}{{.FirstName}} {{.LastName}}{{else}}{{.Username}}{{end}}"

# Always use username
displayname_template: "{{.Username}}"

# Username only, no suffix
displayname_template: "{{.Username}}"
```

If the template fails to render (e.g., syntax error), the raw username is used as a fallback.

## Environment Variables

### Auto-Login

These enable the bridge to connect to Mattermost on first startup without manual bot interaction.

| Variable | Required | Description |
|----------|----------|-------------|
| `MATTERMOST_AUTO_SERVER_URL` | Yes | Mattermost server URL for auto-login |
| `MATTERMOST_AUTO_TOKEN` | Yes | Mattermost access token for auto-login |
| `MATTERMOST_AUTO_OWNER_MXID` | Yes | Matrix user ID that "owns" the auto-login |

All three must be set for auto-login to activate. If any existing logins are found in the bridge database, auto-login is skipped.

Auto-login also triggers `autoSetRelay`, which sets the logged-in user as the relay for all bridged rooms. This is required for the puppet system to function -- without relay, the bridgev2 framework rejects messages from non-logged-in users.

### Puppet Configuration

Each puppet requires a pair of environment variables (and one optional):

| Pattern | Required | Description |
|---------|----------|-------------|
| `MATTERMOST_PUPPET_{SLUG}_MXID` | Yes | Matrix user ID for this puppet |
| `MATTERMOST_PUPPET_{SLUG}_TOKEN` | Yes | Mattermost bot access token |
| `MATTERMOST_PUPPET_{SLUG}_URL` | No | Override server URL (defaults to `server_url`) |

`{SLUG}` is an arbitrary identifier. Convention: uppercase, underscores instead of hyphens.

The bridge scans all environment variables for `MATTERMOST_PUPPET_*_MXID` patterns at startup.

### Relay Bot

| Variable | Required | Description |
|----------|----------|-------------|
| `MATTERMOST_PUPPET_RELAY_TOKEN` | No | Token for the relay bot (if managed as a puppet) |

### Admin API

| Variable | Required | Description |
|----------|----------|-------------|
| `BRIDGE_API_ADDR` | No | Override listen address for admin API (default `:29320`) |

The admin API address resolution order:
1. `admin_api_addr` in config file
2. `BRIDGE_API_ADDR` environment variable
3. Default: `:29320`

## Admin API Endpoints

### `POST /api/reload-puppets`

Reloads the puppet map. Two modes:

**Mode 1: Reload from environment variables**

```bash
curl -X POST http://localhost:29320/api/reload-puppets
```

Re-scans `MATTERMOST_PUPPET_*` environment variables and updates the puppet map.

**Mode 2: Reload from JSON body**

```bash
curl -X POST http://localhost:29320/api/reload-puppets \
  -H 'Content-Type: application/json' \
  -d '[
    {"slug": "ALICE", "mxid": "@alice:example.com", "token": "token-alice"},
    {"slug": "BOB", "mxid": "@bob:example.com", "token": "token-bob"}
  ]'
```

The JSON array represents the **desired state**. Puppets not in the list are removed. Puppets with unchanged tokens are kept without re-authentication.

**Response** (both modes):

```json
{
  "added": 2,
  "removed": 1,
  "total": 5
}
```

| Field | Description |
|-------|-------------|
| `added` | Number of new puppets loaded |
| `removed` | Number of puppets removed |
| `total` | Total puppets now loaded |

## Login Flows

The bridge supports two interactive login methods via the Matrix bot interface:

### Token Login (`token`)

1. User provides Mattermost server URL
2. User provides personal access token
3. Bridge verifies token and creates login

### Password Login (`password`)

1. User provides Mattermost server URL
2. User provides username and password
3. Bridge authenticates and creates login using the session token

Both flows create a `UserLogin` with metadata containing `server_url`, `token`, `user_id`, and `team_id`. After login, the bridge connects the WebSocket and begins syncing channels.

## Network Ports

| Port | Service | Description |
|------|---------|-------------|
| 29319 | Appservice | Matrix homeserver pushes events here (bridge framework default) |
| 29320 | Admin API | Puppet hot-reload endpoint |
