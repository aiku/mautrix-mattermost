#!/usr/bin/env bash
# End-to-end integration test runner for mautrix-mattermost.
# Spins up Synapse + Mattermost + bridge in Docker, bootstraps the full
# chat pipeline, then runs Go integration tests.
#
# Usage:
#   cd testinfra && ./run.sh
#
set -euo pipefail
cd "$(dirname "$0")"

SYNAPSE_URL="http://localhost:18008"
MM_URL="http://localhost:18065"

cleanup() {
  echo "--- Cleaning up ---"
  docker compose down -v 2>/dev/null || true
  rm -rf synapse-data bridge-data .env
}
trap cleanup EXIT

# ── Clean slate ──
cleanup 2>/dev/null || true
mkdir -p synapse-data bridge-data

# ── Generate Synapse signing key ──
echo "=== Generating Synapse signing key ==="
docker run --rm \
  -v "$(pwd)/synapse-data:/data" \
  -e SYNAPSE_SERVER_NAME=localhost \
  -e SYNAPSE_REPORT_STATS=no \
  matrixdotorg/synapse:latest generate 2>/dev/null || true

# Overwrite with our configs (generate clobbers homeserver.yaml)
cp homeserver.yaml synapse-data/homeserver.yaml
cp appservice-mattermost.yaml synapse-data/appservice-mattermost.yaml
cp log.config synapse-data/log.config
cp bridge-config.yaml bridge-data/config.yaml

# ── Phase 1: Start core services ──
echo "=== Starting Synapse + Mattermost ==="
docker compose up -d postgres mattermost synapse

echo "--- Waiting for Synapse ---"
for i in $(seq 1 60); do
  curl -sf "$SYNAPSE_URL/health" >/dev/null 2>&1 && break
  if [ "$i" -eq 60 ]; then
    echo "FAIL: Synapse did not become healthy"
    docker compose logs synapse | tail -30
    exit 1
  fi
  sleep 1
done
echo "Synapse OK"

echo "--- Waiting for Mattermost ---"
for i in $(seq 1 90); do
  curl -sf "$MM_URL/api/v4/system/ping" >/dev/null 2>&1 && break
  if [ "$i" -eq 90 ]; then
    echo "FAIL: Mattermost did not become healthy"
    docker compose logs mattermost | tail -30
    exit 1
  fi
  sleep 1
done
echo "Mattermost OK"

# ── Phase 2a: Register Synapse users for double puppeting ──
echo "=== Registering Synapse users ==="

register_synapse_user() {
  local username="$1"
  local password="$2"
  local admin="$3"

  NONCE=$(curl -sf "$SYNAPSE_URL/_synapse/admin/v1/register" | jq -r '.nonce')
  if [ -z "$NONCE" ] || [ "$NONCE" = "null" ]; then
    echo "WARNING: Could not get registration nonce for $username"
    return
  fi

  if [ "$admin" = "true" ]; then
    MAC=$(printf '%s\0%s\0%s\0admin' "$NONCE" "$username" "$password" \
      | openssl dgst -sha1 -hmac "test-shared-secret" | awk '{print $NF}')
  else
    MAC=$(printf '%s\0%s\0%s\0notadmin' "$NONCE" "$username" "$password" \
      | openssl dgst -sha1 -hmac "test-shared-secret" | awk '{print $NF}')
  fi

  RESP=$(curl -s -X POST "$SYNAPSE_URL/_synapse/admin/v1/register" \
    -H "Content-Type: application/json" \
    -d "{\"nonce\":\"$NONCE\",\"username\":\"$username\",\"password\":\"$password\",\"admin\":$admin,\"mac\":\"$MAC\"}" 2>&1) || true
  if echo "$RESP" | jq -e '.access_token' >/dev/null 2>&1; then
    echo "Registered Synapse user: $username"
  elif echo "$RESP" | jq -e '.errcode == "M_USER_IN_USE"' >/dev/null 2>&1; then
    echo "Synapse user already exists: $username"
  else
    echo "WARNING: Failed to register $username: $RESP"
  fi
}

# admin must exist for auto-login double puppet
register_synapse_user "admin" "adminpass123" "true"
# ceo user for testing double puppet registration via admin API
register_synapse_user "ceo" "ceopass123" "false"

# Agent Synapse users for multi-user double puppeting
for agent in aiku-coo aiku-cto; do
  register_synapse_user "$agent" "${agent}pass123" "false"
done

# ── Phase 2b: Bootstrap Mattermost ──
echo "=== Bootstrapping Mattermost ==="

# Create admin user
curl -sf -X POST "$MM_URL/api/v4/users" \
  -H "Content-Type: application/json" \
  -d '{"email":"admin@test.com","username":"admin","password":"Admin1234!"}' >/dev/null 2>&1 || true

# Login as admin
RESP_HEADERS=$(mktemp)
RESP_BODY=$(curl -sf -X POST "$MM_URL/api/v4/users/login" \
  -H "Content-Type: application/json" \
  -d '{"login_id":"admin","password":"Admin1234!"}' \
  -D "$RESP_HEADERS")
MM_TOKEN=$(grep -i '^token:' "$RESP_HEADERS" | awk '{print $2}' | tr -d '\r\n')
USER_ID=$(echo "$RESP_BODY" | jq -r '.id')
rm -f "$RESP_HEADERS"

if [ -z "$MM_TOKEN" ] || [ "$MM_TOKEN" = "null" ]; then
  echo "FAIL: Could not get Mattermost auth token"
  exit 1
fi
echo "Admin logged in: user_id=$USER_ID"

# Create team
TEAM_RESP=$(curl -sf -X POST "$MM_URL/api/v4/teams" \
  -H "Authorization: Bearer $MM_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"bridge-test","display_name":"Bridge Test","type":"O"}')
TEAM_ID=$(echo "$TEAM_RESP" | jq -r '.id')
echo "Team: $TEAM_ID"

# Create channels
declare -A CHANNEL_IDS
CHANNELS="general-bridge dev-channel test-channel"
for ch in $CHANNELS; do
  DISPLAY=$(echo "$ch" | sed 's/-/ /g' | awk '{for(i=1;i<=NF;i++) $i=toupper(substr($i,1,1)) tolower(substr($i,2))}1')
  CH_RESP=$(curl -sf -X POST "$MM_URL/api/v4/channels" \
    -H "Authorization: Bearer $MM_TOKEN" \
    -H "Content-Type: application/json" \
    -d "{\"team_id\":\"$TEAM_ID\",\"name\":\"$ch\",\"display_name\":\"$DISPLAY\",\"type\":\"O\"}" 2>/dev/null || echo '{"id":""}')
  CH_ID=$(echo "$CH_RESP" | jq -r '.id')
  if [ -n "$CH_ID" ] && [ "$CH_ID" != "" ] && [ "$CH_ID" != "null" ]; then
    CHANNEL_IDS[$ch]=$CH_ID
  fi
done
echo "Channels created: ${!CHANNEL_IDS[*]}"

# Create personal access token for the bridge
PAT_RESP=$(curl -sf -X POST "$MM_URL/api/v4/users/$USER_ID/tokens" \
  -H "Authorization: Bearer $MM_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"description":"bridge-bot"}')
PAT=$(echo "$PAT_RESP" | jq -r '.token')
echo "Bridge PAT: ${PAT:0:8}..."

# Create CEO user (separate from admin to avoid echo prevention)
curl -sf -X POST "$MM_URL/api/v4/users" \
  -H "Content-Type: application/json" \
  -d '{"email":"ceo@test.com","username":"ceo","password":"Ceo12345!"}' >/dev/null 2>&1 || true

CEO_HEADERS=$(mktemp)
CEO_BODY=$(curl -sf -X POST "$MM_URL/api/v4/users/login" \
  -H "Content-Type: application/json" \
  -d '{"login_id":"ceo","password":"Ceo12345!"}' \
  -D "$CEO_HEADERS")
MM_CEO_TOKEN=$(grep -i '^token:' "$CEO_HEADERS" | awk '{print $2}' | tr -d '\r\n')
CEO_USER_ID=$(echo "$CEO_BODY" | jq -r '.id')
rm -f "$CEO_HEADERS"

# Add CEO to team + all channels
curl -sf -X POST "$MM_URL/api/v4/teams/$TEAM_ID/members" \
  -H "Authorization: Bearer $MM_TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"team_id\":\"$TEAM_ID\",\"user_id\":\"$CEO_USER_ID\"}" >/dev/null 2>&1 || true

for ch in $CHANNELS; do
  CH_ID=$(curl -sf "$MM_URL/api/v4/teams/$TEAM_ID/channels/name/$ch" \
    -H "Authorization: Bearer $MM_TOKEN" | jq -r '.id')
  curl -sf -X POST "$MM_URL/api/v4/channels/$CH_ID/members" \
    -H "Authorization: Bearer $MM_TOKEN" \
    -H "Content-Type: application/json" \
    -d "{\"user_id\":\"$CEO_USER_ID\"}" >/dev/null 2>&1 || true
done
echo "CEO user: $CEO_USER_ID"

# Create per-agent bots with puppet tokens
PUPPET_ENV=""
declare -A BOT_TOKENS BOT_USER_IDS
AGENTS="aiku-coo aiku-cto"
for agent in $AGENTS; do
  # Create bot
  BOT_RESP=$(curl -sf -X POST "$MM_URL/api/v4/bots" \
    -H "Authorization: Bearer $MM_TOKEN" \
    -H "Content-Type: application/json" \
    -d "{\"username\":\"$agent\",\"display_name\":\"$agent\"}" 2>/dev/null || echo '{}')
  BOT_USER_ID=$(echo "$BOT_RESP" | jq -r '.user_id // empty')

  if [ -z "$BOT_USER_ID" ]; then
    BOT_USER_ID=$(curl -sf "$MM_URL/api/v4/bots/username/$agent" \
      -H "Authorization: Bearer $MM_TOKEN" 2>/dev/null | jq -r '.user_id // empty')
  fi

  if [ -z "$BOT_USER_ID" ]; then
    echo "WARNING: Could not create/find bot $agent"
    continue
  fi

  # Create access token for the bot
  TOKEN_RESP=$(curl -sf -X POST "$MM_URL/api/v4/users/$BOT_USER_ID/tokens" \
    -H "Authorization: Bearer $MM_TOKEN" \
    -H "Content-Type: application/json" \
    -d '{"description":"puppet"}')
  BOT_TOKEN=$(echo "$TOKEN_RESP" | jq -r '.token // empty')

  if [ -z "$BOT_TOKEN" ]; then
    echo "WARNING: Could not get token for bot $agent"
    continue
  fi

  # Add bot to team + all channels
  curl -sf -X POST "$MM_URL/api/v4/teams/$TEAM_ID/members" \
    -H "Authorization: Bearer $MM_TOKEN" \
    -H "Content-Type: application/json" \
    -d "{\"team_id\":\"$TEAM_ID\",\"user_id\":\"$BOT_USER_ID\"}" >/dev/null 2>&1 || true

  for ch in $CHANNELS; do
    CH_ID=$(curl -sf "$MM_URL/api/v4/teams/$TEAM_ID/channels/name/$ch" \
      -H "Authorization: Bearer $MM_TOKEN" | jq -r '.id')
    curl -sf -X POST "$MM_URL/api/v4/channels/$CH_ID/members" \
      -H "Authorization: Bearer $MM_TOKEN" \
      -H "Content-Type: application/json" \
      -d "{\"user_id\":\"$BOT_USER_ID\"}" >/dev/null 2>&1 || true
  done

  # Build puppet env var: aiku-coo -> COO
  ENV_KEY=$(echo "$agent" | sed 's/^aiku-//' | tr '[:lower:]-' '[:upper:]_')
  PUPPET_ENV="$PUPPET_ENV MATTERMOST_PUPPET_${ENV_KEY}_MXID=@${agent}:localhost"
  PUPPET_ENV="$PUPPET_ENV MATTERMOST_PUPPET_${ENV_KEY}_TOKEN=$BOT_TOKEN"

  BOT_TOKENS[$ENV_KEY]=$BOT_TOKEN
  BOT_USER_IDS[$ENV_KEY]=$BOT_USER_ID

  echo "Bot $agent: user_id=$BOT_USER_ID token=${BOT_TOKEN:0:8}..."
done

# ── Phase 3: Start bridge with puppet tokens ──
echo "=== Starting bridge ==="
export MM_BRIDGE_TOKEN="$PAT"
echo "MM_BRIDGE_TOKEN=$PAT" > .env
for envvar in $PUPPET_ENV; do
  echo "$envvar" >> .env
done
docker compose up -d mautrix-mattermost

echo "--- Waiting for bridge ---"
for i in $(seq 1 60); do
  nc -z localhost 29319 2>/dev/null && break
  if [ "$i" -eq 60 ]; then
    echo "FAIL: Bridge did not start"
    docker compose logs mautrix-mattermost | tail -30
    exit 1
  fi
  sleep 1
done
echo "Bridge OK"

# Give bridge time to discover channels and create portal rooms
echo "--- Waiting for portal rooms (20s) ---"
sleep 20

# ── Phase 4: Dump service logs ──
LOGDIR="$(pwd)/logs"
mkdir -p "$LOGDIR"
docker compose logs synapse              > "$LOGDIR/synapse.log" 2>&1
docker compose logs mattermost           > "$LOGDIR/mattermost.log" 2>&1
docker compose logs mautrix-mattermost   > "$LOGDIR/mautrix-mattermost.log" 2>&1
echo "=== Service logs saved to $LOGDIR/ ==="

# ── Phase 5: Run Go integration tests ──
echo "=== Running integration tests ==="
TEST_EXIT=0
MM_URL="$MM_URL" \
MM_TOKEN="$MM_TOKEN" \
MM_CEO_TOKEN="$MM_CEO_TOKEN" \
MM_TEAM_ID="$TEAM_ID" \
SYNAPSE_URL="$SYNAPSE_URL" \
MM_COO_BOT_TOKEN="${BOT_TOKENS[COO]:-}" \
MM_CTO_BOT_TOKEN="${BOT_TOKENS[CTO]:-}" \
MM_COO_BOT_USER_ID="${BOT_USER_IDS[COO]:-}" \
MM_CTO_BOT_USER_ID="${BOT_USER_IDS[CTO]:-}" \
MM_CEO_USER_ID="$CEO_USER_ID" \
MM_ADMIN_USER_ID="$USER_ID" \
BRIDGE_ADMIN_URL="http://localhost:29320" \
  go test -v -count=1 -timeout 300s ./... || TEST_EXIT=$?

# Dump logs again after tests
docker compose logs synapse              > "$LOGDIR/synapse.log" 2>&1
docker compose logs mattermost           > "$LOGDIR/mattermost.log" 2>&1
docker compose logs mautrix-mattermost   > "$LOGDIR/mautrix-mattermost.log" 2>&1
echo "=== Post-test logs saved to $LOGDIR/ ==="

if [ "$TEST_EXIT" -eq 0 ]; then
  echo "=== ALL TESTS PASSED ==="
else
  echo "=== TESTS FAILED (exit $TEST_EXIT) — check logs in $LOGDIR/ ==="
  exit "$TEST_EXIT"
fi
