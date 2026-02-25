# Multi-Agent Puppeting Demo

A self-contained demo that starts 3 AI agents, each posting to Mattermost
under their own distinct bot identity through the mautrix-mattermost bridge
puppet system.

```
Agent "Researcher"  --Matrix-->  Bridge  --puppet-->  Mattermost (as researcher bot)
Agent "Coder"       --Matrix-->  Bridge  --puppet-->  Mattermost (as coder bot)
Agent "Reviewer"    --Matrix-->  Bridge  --puppet-->  Mattermost (as reviewer bot)
```

This is the same pattern used by [aiku/os](https://github.com/aiku/os) to run
32+ autonomous AI agents with distinct Mattermost identities.

## Quick Start

```bash
cd example/multi-agent-puppeting
docker compose up --build
```

After ~2 minutes, open **http://localhost:18065** and log in:

| Field    | Value           |
|----------|-----------------|
| Username | `admin`         |
| Password | `DemoAdmin123!` |

Navigate to the **~town-square** channel in the **demo** team. You'll see
3 messages, each from a different agent identity:

- **Researcher** -- analyzes requirements
- **Coder** -- proposes implementation
- **Reviewer** -- provides code review

## What's Happening

### Architecture

```
┌──────────────────────────────────────────────────────────────────┐
│  docker compose up                                                │
│                                                                   │
│  ┌──────────┐  ┌────────────┐  ┌──────────┐  ┌───────────────┐  │
│  │ Postgres │──│ Mattermost │  │  Synapse  │  │ Agent Demo    │  │
│  │  :5432   │  │   :8065    │  │   :8008   │  │ (Python)      │  │
│  └──────────┘  └─────┬──────┘  └─────┬─────┘  └──────┬────────┘  │
│                      │               │               │            │
│                      │               │    posts as    │            │
│                      │               │  @agent-*:localhost         │
│                      │               │               │            │
│                ┌─────┴───────────────┴───────────────┘            │
│                │  mautrix-mattermost Bridge                       │
│                │  :29319 (appservice) :29320 (admin API)          │
│                │                                                  │
│                │  Puppet Router:                                   │
│                │    @agent-researcher:localhost → researcher bot   │
│                │    @agent-coder:localhost      → coder bot        │
│                │    @agent-reviewer:localhost   → reviewer bot     │
│                │    (no match)                 → relay fallback    │
│                └──────────────────────────────────────────────────┘│
│                                                                   │
│  ┌──── Init (runs once) ────────────────────────────────────────┐ │
│  │  1. Create MM admin, team, town-square                       │ │
│  │  2. Create 3 MM bot accounts + generate tokens               │ │
│  │  3. Register 3 Synapse users (@agent-*:localhost)             │ │
│  │  4. Write puppet env vars → /bridge-data/env.sh              │ │
│  └──────────────────────────────────────────────────────────────┘ │
└──────────────────────────────────────────────────────────────────┘
```

### Message Pipeline

When the agent demo posts a message:

1. **Agent** sends message to Matrix room as `@agent-researcher:localhost`
   (via Synapse appservice API)
2. **Synapse** delivers the event to the bridge portal room
3. **Bridge** receives the event, extracts sender MXID
4. **Puppet router** (`resolvePostClient`) looks up `@agent-researcher:localhost`
   in the puppet map → finds the researcher bot token
5. **Bridge** posts to Mattermost using that bot token
6. **Mattermost** shows the message under the "Researcher" bot identity

### Startup Sequence

```
postgres (healthy)
  → mattermost (healthy) + synapse (healthy)
    → init (creates bots, tokens, users, writes env.sh)
      → mautrix-mattermost (sources env.sh, connects to MM, creates portal rooms)
        → agents (waits for portal rooms, posts demo conversation)
```

## Files

| File | Purpose |
|------|---------|
| `docker-compose.yml` | Orchestrates all 6 services |
| `init/init.sh` | Bootstrap: creates MM bots, Synapse users, puppet config |
| `init/agents.conf` | Agent definitions (slug, display name, description) |
| `agents/demo.py` | Python agent app: posts via Matrix puppet pipeline |
| `bridge/config.yaml` | Pre-configured bridge config for the demo |
| `bridge/Dockerfile` | Alpine-based bridge image (needs shell for env sourcing) |
| `bridge/entrypoint.sh` | Sources puppet env vars at startup |
| `synapse/Dockerfile` | Auto-configuring Synapse with appservice registrations |
| `synapse/entrypoint.sh` | Generates + patches Synapse config on first run |
| `synapse/bridge-registration.yaml` | Bridge appservice registration |
| `synapse/agent-registration.yaml` | Agent demo appservice registration |

## Adding More Agents

### Via agents.conf

Edit `init/agents.conf` to add agents:

```
# slug:display_name:description
researcher:Researcher:AI research and analysis agent
coder:Coder:AI coding and implementation agent
reviewer:Reviewer:AI code review and feedback agent
planner:Planner:AI project planning agent
```

Then rebuild:

```bash
docker compose down -v
docker compose up --build
```

### Via Hot-Reload API (no restart)

After the demo is running, add agents dynamically:

```bash
# Create a new MM bot and get a token (using the admin session)
# Then reload puppets via the bridge API:
curl -X POST http://localhost:19320/api/reload-puppets \
  -H "Content-Type: application/json" \
  -d '[
    {"slug": "PLANNER", "mxid": "@agent-planner:localhost", "token": "<bot-token>"}
  ]'
```

## Adapting for Production

This demo uses hardcoded tokens and SQLite. For production:

1. **Replace demo tokens** — generate real `as_token`/`hs_token` with the bridge's
   `-g` flag: `mautrix-mattermost -g -c config.yaml -r registration.yaml`
2. **Use PostgreSQL** — change `database.type` to `postgres` in bridge config
3. **Secure the admin API** — see [GitHub issue #1](https://github.com/aiku/mautrix-mattermost/issues/1)
4. **Use Kubernetes** — see [doc/puppet-deployment-guide.md](../../doc/puppet-deployment-guide.md)
   for ConfigMap/Secret patterns and init job examples
5. **Connect real LLM agents** — replace `demo.py` with your agent framework
   (LangGraph, CrewAI, AutoGen, etc.) using the `MatrixAgentSender` pattern

## Cleanup

```bash
docker compose down -v
```

The `-v` flag removes all data volumes (Postgres, Mattermost, Synapse, bridge).
