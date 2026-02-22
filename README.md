# mautrix-mattermost

[![Go Report Card](https://goreportcard.com/badge/github.com/aiku/mautrix-mattermost)](https://goreportcard.com/report/github.com/aiku/mautrix-mattermost)
[![CI](https://github.com/aiku/mautrix-mattermost/actions/workflows/ci.yml/badge.svg)](https://github.com/aiku/mautrix-mattermost/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/aiku/mautrix-mattermost.svg)](https://pkg.go.dev/github.com/aiku/mautrix-mattermost)
[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](LICENSE)

A Matrix-Mattermost bridge built on the [mautrix](https://github.com/mautrix/go) bridgev2 framework with **puppet identity routing** -- each Matrix user can post to Mattermost under their own dedicated bot account, preserving individual identity across platforms.

## Features

- **Puppet Identity Routing** -- Map Matrix users to dedicated Mattermost bot accounts. Messages appear under the bot's name and avatar, not a generic relay user.
- **Hot-Reload API** -- Add or remove puppet mappings at runtime via `POST /api/reload-puppets` without restarting the bridge.
- **Bidirectional Messaging** -- Full two-way sync: text, images, video, audio, files, reactions, edits, deletes, typing indicators, and read receipts.
- **Rich Formatting** -- Converts Matrix HTML to Mattermost markdown and back (bold, italic, code blocks, links, lists, blockquotes, headings).
- **Echo Prevention** -- Multi-layer filtering prevents infinite message loops: puppet bot filtering, bridge bot filtering, relay bot filtering, and configurable username prefix filtering.
- **Auto Portal Relay** -- Background goroutine watches for new Matrix portal rooms and automatically enables relay mode.
- **Multiple Auth Methods** -- Token-based and password-based Mattermost login flows.
- **bridgev2 Native** -- Built on mautrix bridgev2 for robust Matrix protocol handling, encryption support, and proven reliability.

## Architecture

```
Matrix Homeserver                          Mattermost Server
      |                                          |
      v                                          v
  [mautrix-mattermost bridge]                    |
      |                                          |
      +-- Puppet Router ----+                    |
      |   Maps @user:hs --> |-- Bot Token A ---->|  (posts as Bot A)
      |   to MM bot tokens  |-- Bot Token B ---->|  (posts as Bot B)
      |                     |-- Bot Token C ---->|  (posts as Bot C)
      |                     +                    |
      +-- Relay Fallback ----------------------->|  (posts as relay bot)
      |                                          |
      +-- Event Stream <-------------------------|  (WebSocket listener)
```

When a Matrix user sends a message:

1. The bridge receives the Matrix event via appservice.
2. The **puppet router** checks if the sender's MXID has a mapped Mattermost bot token.
3. If matched, the message is posted to Mattermost using that bot's token -- it appears under the bot's identity.
4. If no puppet match, the message goes through the standard relay bot.

When a Mattermost user posts:

1. The bridge receives the event via WebSocket.
2. Echo prevention filters out messages from bridge bots and puppet bots.
3. The message is relayed to the corresponding Matrix room.

## Quick Start

### Docker

```bash
docker run -d \
  --name mautrix-mattermost \
  -v /path/to/config:/data \
  -p 29319:29319 \
  ghcr.io/aiku/mautrix-mattermost:latest
```

### Docker Compose

See [docker-compose.yml](docker-compose.yml) for a full example stack with Mattermost, Synapse, and PostgreSQL.

```bash
# Copy and edit configuration
cp config.example.yaml config/config.yaml
# Edit config.yaml with your settings

# Start the stack
docker compose up -d
```

### From Source

```bash
# Clone
git clone https://github.com/aiku/mautrix-mattermost.git
cd mautrix-mattermost

# Build
make build

# Run
./mautrix-mattermost -c config.yaml
```

## Configuration

### Config File

The bridge is configured via `config.yaml`. Key sections:

```yaml
# Homeserver connection
homeserver:
  address: https://matrix.example.com
  domain: example.com

# Appservice registration
appservice:
  address: http://localhost:29319
  hostname: 0.0.0.0
  port: 29319
  id: mattermost
  bot:
    username: mattermost-bridge
    displayname: Mattermost Bridge

# Bridge behavior
bridge:
  relay:
    enabled: true
    message_formats:
      m.text: "<b>{{ .Sender.Displayname }}</b>: {{ .Message }}"
      m.notice: "<b>{{ .Sender.Displayname }}</b>: {{ .Message }}"
      m.emote: "* <b>{{ .Sender.Displayname }}</b> {{ .Message }}"
      m.file: "<b>{{ .Sender.Displayname }}</b> sent a file: {{ .Message }}"
      m.image: "<b>{{ .Sender.Displayname }}</b> sent an image: {{ .Message }}"
      m.audio: "<b>{{ .Sender.Displayname }}</b> sent audio: {{ .Message }}"
      m.video: "<b>{{ .Sender.Displayname }}</b> sent a video: {{ .Message }}"
  permissions:
    "*": relay
    "example.com": user
    "@admin:example.com": admin
```

### Environment Variables

| Variable | Description | Example |
|----------|-------------|---------|
| `MATTERMOST_URL` | Mattermost server URL | `http://mattermost:8065` |
| `MATTERMOST_AUTO_SERVER_URL` | Auto-login server URL | `http://mattermost:8065` |
| `MATTERMOST_AUTO_OWNER_MXID` | Auto-login Matrix user | `@admin:example.com` |
| `MATTERMOST_AUTO_TOKEN` | Auto-login bot token | `abc123...` |
| `MATTERMOST_PUPPET_{SLUG}_MXID` | Puppet Matrix user ID | `@bot-user:example.com` |
| `MATTERMOST_PUPPET_{SLUG}_TOKEN` | Puppet Mattermost bot token | `xyz789...` |

### Puppet System

The puppet identity system maps Matrix user IDs to Mattermost bot accounts via environment variables.

#### Setup

1. Create a Mattermost bot account for each identity you want to puppet.
2. Generate a token for each bot.
3. Set environment variables following the naming convention:

```bash
# Pattern: MATTERMOST_PUPPET_{SLUG}_MXID and MATTERMOST_PUPPET_{SLUG}_TOKEN
# SLUG: uppercase, hyphens replaced with underscores

export MATTERMOST_PUPPET_ALICE_MXID="@alice:example.com"
export MATTERMOST_PUPPET_ALICE_TOKEN="bot-token-for-alice"

export MATTERMOST_PUPPET_BOB_SMITH_MXID="@bob-smith:example.com"
export MATTERMOST_PUPPET_BOB_SMITH_TOKEN="bot-token-for-bob"
```

4. Start or restart the bridge. Puppets are loaded on startup.

#### Hot-Reload API

Add or remove puppets at runtime without restarting the bridge:

```bash
# Reload from current environment variables
curl -X POST http://localhost:29319/api/reload-puppets

# Reload with explicit puppet entries
curl -X POST http://localhost:29319/api/reload-puppets \
  -H "Content-Type: application/json" \
  -d '{
    "puppets": [
      {"slug": "ALICE", "mxid": "@alice:example.com", "token": "bot-token"},
      {"slug": "BOB", "mxid": "@bob:example.com", "token": "bot-token"}
    ]
  }'
```

The response includes the count of active puppets and any errors encountered.

## Development

### Prerequisites

- Go 1.22 or later
- Docker (for integration tests)
- golangci-lint (for linting)

### Build and Test

```bash
# Build
make build

# Run all tests
make test

# Run tests with race detector
make test-race

# Lint
make lint

# Format
make fmt
```

### Project Structure

```
.
├── cmd/
│   └── mautrix-mattermost/    # CLI entrypoint
├── pkg/
│   ├── connector/              # bridgev2 NetworkConnector implementation
│   ├── matterclient/           # Mattermost API + WebSocket client
│   ├── matterformat/           # HTML <-> Markdown conversion
│   └── puppet/                 # Puppet identity routing
├── deploy/                     # Kubernetes manifests
├── config.example.yaml         # Example configuration
├── Dockerfile                  # Multi-stage container build
├── docker-compose.yml          # Example full stack
└── Makefile                    # Build automation
```

## Acknowledgments

- [mautrix/go](https://github.com/mautrix/go) -- The bridgev2 framework that powers this bridge
- [Mattermost](https://mattermost.com/) -- Open source messaging platform
- [Matrix](https://matrix.org/) -- Open standard for decentralized communication

## License

Apache License 2.0. See [LICENSE](LICENSE) for details.
