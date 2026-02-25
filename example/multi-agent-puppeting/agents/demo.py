"""Multi-Agent Puppeting Demo — Agent Application.

Demonstrates 3 LLM agents posting to Mattermost with distinct identities
through the mautrix-mattermost bridge puppet system.

Pipeline:
  Agent posts to Matrix as @agent-{name}:localhost (via appservice API)
    -> mautrix-mattermost bridge sees the MXID in the portal room
    -> Bridge looks up puppet mapping: MXID -> MM bot token
    -> Bridge posts to Mattermost using that bot's token
    -> Message appears under the agent's own identity in Mattermost

This is the same pattern used by aiku/os for 32+ concurrent AI agents.
See: github.com/aiku/mautrix-mattermost/doc/puppet-system.md
"""

from __future__ import annotations

import asyncio
import logging
import os
import re
import time
from http.server import HTTPServer, BaseHTTPRequestHandler
from threading import Thread
from urllib.parse import quote

import httpx

logger = logging.getLogger(__name__)

# ── Configuration ────────────────────────────────────────────────────────────

SYNAPSE_URL = os.environ.get("SYNAPSE_URL", "http://synapse:8008")
SERVER_NAME = os.environ.get("SERVER_NAME", "localhost")
AGENT_AS_TOKEN = os.environ.get("AGENT_AS_TOKEN", "demo_agent_as_token_2024")
BRIDGE_AS_TOKEN = os.environ.get("BRIDGE_AS_TOKEN", "demo_bridge_as_token_2024")
BRIDGE_BOT_MXID = f"@mattermostbot:{SERVER_NAME}"
SYNAPSE_ADMIN_PASSWORD = os.environ.get("SYNAPSE_ADMIN_PASSWORD", "DemoAdmin123!")

# ── Minimal Appservice Server ────────────────────────────────────────────────
# Synapse pushes events to registered appservices. We serve a minimal endpoint
# so Synapse doesn't log connection errors. The demo doesn't need to receive
# events — it only sends.


class _AppserviceHandler(BaseHTTPRequestHandler):
    """Accept all appservice transactions with 200 OK."""

    def do_PUT(self):
        length = int(self.headers.get("Content-Length", 0))
        if length:
            self.rfile.read(length)
        self.send_response(200)
        self.end_headers()
        self.wfile.write(b"{}")

    def do_GET(self):
        self.send_response(200)
        self.end_headers()
        self.wfile.write(b"{}")

    def log_message(self, format, *args):
        pass  # Suppress request logs


def _start_appservice_server():
    """Start a background HTTP server for appservice transaction receipts."""
    server = HTTPServer(("0.0.0.0", 8080), _AppserviceHandler)
    thread = Thread(target=server.serve_forever, daemon=True)
    thread.start()
    logger.info("Appservice endpoint listening on :8080")


# ── Matrix Agent Sender ──────────────────────────────────────────────────────
# Simplified version of the MatrixSender from aiku/os agents/src/bridges/.
# Posts messages to Matrix rooms as agent MXIDs using appservice impersonation.


class MatrixAgentSender:
    """Send messages to Matrix rooms as agent MXIDs.

    Uses the agent appservice token to impersonate @agent-{name}:localhost
    MXIDs. When these messages land in bridge portal rooms, the bridge's
    puppet router resolves the MXID to a Mattermost bot token and posts
    under that bot's identity.
    """

    def __init__(self):
        self._registered: set[str] = set()
        self._joined: set[tuple[str, str]] = set()
        self._room_cache: dict[str, str] = {}
        self._admin_token: str | None = None

    def _mxid(self, agent_name: str) -> str:
        return f"@agent-{agent_name}:{SERVER_NAME}"

    def _headers(self) -> dict[str, str]:
        return {
            "Authorization": f"Bearer {AGENT_AS_TOKEN}",
            "Content-Type": "application/json",
        }

    async def _get_admin_token(self) -> str:
        """Get a Synapse admin token via password login (cached)."""
        if self._admin_token:
            return self._admin_token

        async with httpx.AsyncClient(base_url=SYNAPSE_URL, timeout=10.0) as client:
            resp = await client.post(
                "/_matrix/client/v3/login",
                json={
                    "type": "m.login.password",
                    "identifier": {"type": "m.id.user", "user": "admin"},
                    "password": SYNAPSE_ADMIN_PASSWORD,
                },
            )
            if resp.status_code == 200:
                self._admin_token = resp.json()["access_token"]
                return self._admin_token
            raise RuntimeError(f"Admin login failed: {resp.status_code} {resp.text}")

    async def register_agent(self, agent_name: str, display_name: str) -> None:
        """Register an agent MXID with Synapse (idempotent).

        Uses the appservice registration API — no password needed.
        """
        if agent_name in self._registered:
            return

        localpart = f"agent-{agent_name}"

        async with httpx.AsyncClient(base_url=SYNAPSE_URL, timeout=10.0) as client:
            resp = await client.post(
                "/_matrix/client/v3/register",
                headers=self._headers(),
                json={
                    "type": "m.login.application_service",
                    "username": localpart,
                },
            )

            # 200 = new registration, 400 M_USER_IN_USE = already exists
            if resp.status_code == 400:
                error = resp.json()
                if error.get("errcode") != "M_USER_IN_USE":
                    raise RuntimeError(f"Failed to register {localpart}: {resp.text}")

            # Set display name
            mxid = self._mxid(agent_name)
            await client.put(
                f"/_matrix/client/v3/profile/{quote(mxid)}/displayname"
                f"?user_id={quote(mxid)}",
                headers=self._headers(),
                json={"displayname": display_name},
            )

        self._registered.add(agent_name)
        logger.info("Registered agent: %s (%s)", agent_name, self._mxid(agent_name))

    async def find_portal_room(self, channel_name: str) -> str | None:
        """Find a bridge portal room by Mattermost channel name.

        Bridge portal rooms are created by @mattermostbot:localhost.
        We match by normalizing the room name to a channel slug.
        """
        if channel_name in self._room_cache:
            return self._room_cache[channel_name]

        admin_token = await self._get_admin_token()

        async with httpx.AsyncClient(base_url=SYNAPSE_URL, timeout=15.0) as client:
            resp = await client.get(
                "/_synapse/admin/v1/rooms",
                headers={"Authorization": f"Bearer {admin_token}"},
                params={"limit": 200},
            )
            if resp.status_code != 200:
                logger.warning("Room list failed: %d", resp.status_code)
                return None

            rooms = resp.json().get("rooms", [])

            for room in rooms:
                name = room.get("name") or ""
                creator = room.get("creator") or ""
                room_id = room.get("room_id") or ""

                if creator != BRIDGE_BOT_MXID:
                    continue

                # Normalize: "Town Square" -> "town-square"
                normalized = re.sub(r"[^a-z0-9-]", "", name.lower().replace(" ", "-"))

                if normalized == channel_name:
                    self._room_cache[channel_name] = room_id
                    logger.info(
                        "Found portal room for ~%s: %s (name=%s)",
                        channel_name, room_id, name,
                    )
                    return room_id

        return None

    async def ensure_joined(self, agent_name: str, room_id: str) -> None:
        """Join an agent to a room, requesting invite from bridge bot if needed.

        Bridge portal rooms are invite-only. The bridge bot must invite the
        agent MXID before it can join. We use the bridge's appservice token
        to impersonate the bridge bot and send the invite.
        """
        key = (agent_name, room_id)
        if key in self._joined:
            return

        mxid = self._mxid(agent_name)

        async with httpx.AsyncClient(base_url=SYNAPSE_URL, timeout=10.0) as client:
            # Try direct join
            resp = await client.post(
                f"/_matrix/client/v3/rooms/{quote(room_id)}/join"
                f"?user_id={quote(mxid)}",
                headers=self._headers(),
                json={},
            )

            if resp.status_code == 200:
                self._joined.add(key)
                logger.info("Agent %s joined %s", agent_name, room_id)
                return

            # Direct join failed — invite via bridge bot
            logger.info("Requesting invite for %s from bridge bot...", mxid)

            await client.post(
                f"/_matrix/client/v3/rooms/{quote(room_id)}/invite"
                f"?user_id={quote(BRIDGE_BOT_MXID)}",
                headers={
                    "Authorization": f"Bearer {BRIDGE_AS_TOKEN}",
                    "Content-Type": "application/json",
                },
                json={"user_id": mxid},
            )

            # Retry join after invite
            resp2 = await client.post(
                f"/_matrix/client/v3/rooms/{quote(room_id)}/join"
                f"?user_id={quote(mxid)}",
                headers=self._headers(),
                json={},
            )

            if resp2.status_code == 200:
                self._joined.add(key)
                logger.info("Agent %s joined %s after invite", agent_name, room_id)
            else:
                logger.warning(
                    "Join failed for %s: %d %s",
                    mxid, resp2.status_code, resp2.text[:200],
                )

    async def send_message(
        self,
        agent_name: str,
        room_id: str,
        message: str,
    ) -> str:
        """Send a message to a Matrix room as an agent.

        The message flows through the puppet pipeline:
          Matrix (as @agent-{name}:localhost)
            -> bridge sees MXID in portal room
            -> resolves puppet: MXID -> MM bot token
            -> posts to Mattermost as the agent's bot
        """
        await self.register_agent(agent_name, agent_name.title())
        await self.ensure_joined(agent_name, room_id)

        mxid = self._mxid(agent_name)
        txn_id = f"{agent_name}-{int(time.time() * 1000)}"

        async with httpx.AsyncClient(base_url=SYNAPSE_URL, timeout=10.0) as client:
            resp = await client.put(
                f"/_matrix/client/v3/rooms/{quote(room_id)}"
                f"/send/m.room.message/{txn_id}"
                f"?user_id={quote(mxid)}",
                headers=self._headers(),
                json={
                    "msgtype": "m.text",
                    "body": message,
                },
            )

            if resp.status_code not in (200, 201):
                raise RuntimeError(
                    f"Send failed as {agent_name}: {resp.status_code} {resp.text}"
                )

            event_id = resp.json().get("event_id", "")
            logger.info("Sent message as %s (event: %s)", agent_name, event_id)
            return event_id


# ── Agent Definitions ────────────────────────────────────────────────────────

AGENTS = {
    "researcher": {
        "display_name": "Researcher",
        "description": "AI research and analysis agent",
    },
    "coder": {
        "display_name": "Coder",
        "description": "AI coding and implementation agent",
    },
    "reviewer": {
        "display_name": "Reviewer",
        "description": "AI code review and feedback agent",
    },
}

# ── Demo Conversation ────────────────────────────────────────────────────────
# A simulated multi-agent collaboration. Each message is posted by a different
# agent and appears under that agent's own identity in Mattermost.

CONVERSATION = [
    (
        "researcher",
        "I've analyzed the project requirements. Key findings:\n\n"
        "1. **Real-time sync** needed between platforms (WebSocket recommended)\n"
        "2. **Identity preservation** -- each agent must post under its own name\n"
        "3. **Echo prevention** critical to avoid infinite message loops\n"
        "4. **Hot-reload** capability for adding agents without downtime\n\n"
        "Recommendation: puppet routing pattern with per-user token mapping.",
    ),
    (
        "coder",
        "Thanks for the analysis. Here's my implementation plan:\n\n"
        "```go\n"
        "func (m *MattermostClient) resolvePostClient(\n"
        "    origSender *bridgev2.OrigSender,\n"
        "    evt *event.Event,\n"
        ") (*model.Client4, string) {\n"
        "    // Path 1: relay metadata (primary puppet path)\n"
        "    if puppet, ok := m.connector.Puppets[origSender.UserID]; ok {\n"
        "        return puppet.Client, puppet.UserID\n"
        "    }\n"
        "    // Path 2: direct event sender\n"
        "    if puppet, ok := m.connector.Puppets[evt.Sender]; ok {\n"
        "        return puppet.Client, puppet.UserID\n"
        "    }\n"
        "    // Path 3: relay fallback\n"
        "    return m.client, m.userID\n"
        "}\n"
        "```\n\n"
        "Key components:\n"
        "- WebSocket listener with auto-reconnect\n"
        "- O(1) MXID -> bot token lookup via sync.RWMutex map\n"
        "- `POST /api/reload-puppets` for runtime puppet changes\n"
        "- Relay fallback ensures no messages are lost",
    ),
    (
        "reviewer",
        "Code review on the proposed architecture:\n\n"
        "**Approved** with conditions:\n\n"
        "- Echo prevention needs 4+ layers "
        "(bridge bot, system msgs, puppet IDs, prefix check)\n"
        "- Puppet map requires `sync.RWMutex` for concurrent reload safety\n"
        "- Bot tokens must **never** appear in logs or error messages\n"
        "- Add graceful degradation when puppet auth fails at reload time\n\n"
        "This is a solid design. The 3-path puppet resolution cleanly separates "
        "identity management from message handling. "
        "The hot-reload API avoids downtime when adding new agents.",
    ),
]


# ── Main ─────────────────────────────────────────────────────────────────────


async def wait_for_services():
    """Wait for Synapse and the bridge to be ready."""
    logger.info("Waiting for Synapse...")

    for attempt in range(90):
        try:
            async with httpx.AsyncClient(timeout=5.0) as client:
                resp = await client.get(f"{SYNAPSE_URL}/_matrix/client/versions")
                if resp.status_code == 200:
                    logger.info("Synapse is ready.")
                    return
        except Exception:
            pass
        await asyncio.sleep(2)

    raise RuntimeError("Synapse didn't become ready in time")


async def wait_for_portal_rooms(sender: MatrixAgentSender) -> str:
    """Wait for the bridge to create portal rooms after connecting to MM."""
    logger.info("Waiting for bridge to create portal rooms...")
    logger.info(
        "(The bridge needs to connect to Mattermost and sync channels first)"
    )

    for attempt in range(60):
        room_id = await sender.find_portal_room("town-square")
        if room_id:
            return room_id

        if attempt % 6 == 0:
            logger.info(
                "  Still waiting for portal rooms... (attempt %d/60)", attempt + 1
            )
        await asyncio.sleep(5)

    raise RuntimeError(
        "Bridge didn't create portal rooms. "
        "Check mautrix-mattermost logs with: docker compose logs mautrix-mattermost"
    )


async def run_demo():
    """Run the multi-agent puppeting demo."""
    logger.info("=" * 60)
    logger.info("  Multi-Agent Puppeting Demo")
    logger.info("  3 agents, 3 identities, 1 Mattermost channel")
    logger.info("=" * 60)

    await wait_for_services()

    sender = MatrixAgentSender()

    # Register all agent MXIDs with Synapse
    for name, info in AGENTS.items():
        await sender.register_agent(name, info["display_name"])

    # Wait for bridge portal rooms
    room_id = await wait_for_portal_rooms(sender)
    logger.info("Portal room ready: %s", room_id)

    # Small delay to ensure bridge relay is fully configured
    logger.info("Waiting for bridge relay setup...")
    await asyncio.sleep(15)

    # Run the demo conversation
    logger.info("")
    logger.info("=" * 60)
    logger.info("  Starting multi-agent conversation")
    logger.info("  Open http://localhost:18065 to watch live!")
    logger.info("  Login: admin / DemoAdmin123!")
    logger.info("=" * 60)
    logger.info("")

    for agent_name, message in CONVERSATION:
        display = AGENTS[agent_name]["display_name"]
        logger.info("[%s] Posting message...", display)

        try:
            await sender.send_message(agent_name, room_id, message)
            logger.info("[%s] Message delivered via puppet pipeline.", display)
        except Exception:
            logger.exception("[%s] Failed to send message", display)

        # Pause between messages for readability
        await asyncio.sleep(4)

    logger.info("")
    logger.info("=" * 60)
    logger.info("  Demo complete!")
    logger.info("")
    logger.info("  Each message appears under a different bot identity")
    logger.info("  in Mattermost ~town-square.")
    logger.info("")
    logger.info("  Pipeline: Agent -> Matrix -> Bridge -> Mattermost")
    logger.info("  (same pattern used by aiku/os for 32+ AI agents)")
    logger.info("")
    logger.info("  Try the hot-reload API:")
    logger.info("    curl -X POST http://localhost:19320/api/reload-puppets")
    logger.info("=" * 60)

    # Keep running so the container stays up (useful for exec-ing in)
    logger.info("Agent demo is idle. Press Ctrl+C to stop.")
    try:
        while True:
            await asyncio.sleep(60)
    except asyncio.CancelledError:
        pass


def main():
    logging.basicConfig(
        level=logging.INFO,
        format="%(asctime)s %(levelname)s %(message)s",
        datefmt="%H:%M:%S",
    )

    # Start minimal appservice HTTP server in background
    _start_appservice_server()

    asyncio.run(run_demo())


if __name__ == "__main__":
    main()
