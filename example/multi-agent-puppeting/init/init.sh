#!/usr/bin/env bash
# =============================================================================
# Multi-Agent Puppeting Demo — Init Script
#
# Bootstraps Mattermost and Synapse for the demo:
#   1. Creates MM admin user, team, and channels
#   2. Creates MM bot accounts for each agent (from agents.conf)
#   3. Generates personal access tokens
#   4. Registers corresponding Synapse (Matrix) users
#   5. Writes puppet environment variables to /bridge-data/env.sh
#
# Inspired by aiku/os deploy/init/add-agent.sh
# =============================================================================
set -uo pipefail

# ── Configuration ────────────────────────────────────────────────────────────

MM_URL="${MM_URL:-http://mattermost:8065}"
SYNAPSE_URL="${SYNAPSE_URL:-http://synapse:8008}"
MM_ADMIN_USER="admin"
MM_ADMIN_EMAIL="admin@example.com"
MM_ADMIN_PASSWORD="${MM_ADMIN_PASSWORD:-DemoAdmin123!}"
SYNAPSE_SHARED_SECRET="${SYNAPSE_SHARED_SECRET:-demo_synapse_shared_secret}"
SERVER_NAME="${SERVER_NAME:-localhost}"
TEAM_NAME="${TEAM_NAME:-demo}"
AGENTS_CONF="${AGENTS_CONF:-/agents.conf}"
ENV_OUTPUT="/bridge-data/env.sh"

# ── Helpers ──────────────────────────────────────────────────────────────────

log()  { echo "[init] $(date '+%H:%M:%S') $*"; }
warn() { echo "[init] $(date '+%H:%M:%S') WARN: $*" >&2; }
die()  { echo "[init] $(date '+%H:%M:%S') FATAL: $*" >&2; exit 1; }

mm_api() {
  local method="$1" path="$2"
  shift 2
  curl -s -X "$method" \
    -H "Authorization: Bearer ${MM_TOKEN:-}" \
    -H "Content-Type: application/json" \
    "${MM_URL}/api/v4${path}" "$@" 2>/dev/null
}

# ── Step 1: Wait for services ────────────────────────────────────────────────

log "Waiting for Mattermost at ${MM_URL}..."
for i in $(seq 1 60); do
  if curl -sf "${MM_URL}/api/v4/system/ping" > /dev/null 2>&1; then
    break
  fi
  sleep 2
done
curl -sf "${MM_URL}/api/v4/system/ping" > /dev/null 2>&1 || die "Mattermost not ready"
log "Mattermost is ready."

log "Waiting for Synapse at ${SYNAPSE_URL}..."
for i in $(seq 1 60); do
  if curl -sf "${SYNAPSE_URL}/health" > /dev/null 2>&1; then
    break
  fi
  sleep 2
done
curl -sf "${SYNAPSE_URL}/health" > /dev/null 2>&1 || die "Synapse not ready"
log "Synapse is ready."

# ── Step 2: Create Mattermost admin user ─────────────────────────────────────

log "Creating Mattermost admin user..."
curl -sf -X POST "${MM_URL}/api/v4/users" \
  -H "Content-Type: application/json" \
  -d "{
    \"email\": \"${MM_ADMIN_EMAIL}\",
    \"username\": \"${MM_ADMIN_USER}\",
    \"password\": \"${MM_ADMIN_PASSWORD}\"
  }" > /dev/null 2>&1 || log "Admin user may already exist"

# Log in
log "Logging in as ${MM_ADMIN_USER}..."
tmpfile=$(mktemp)
curl -s -X POST "${MM_URL}/api/v4/users/login" \
  -H "Content-Type: application/json" \
  -d "{\"login_id\": \"${MM_ADMIN_USER}\", \"password\": \"${MM_ADMIN_PASSWORD}\"}" \
  -D "$tmpfile" > /dev/null 2>&1

MM_TOKEN=$(grep -i '^token:' "$tmpfile" | awk '{print $2}' | tr -d '\r\n')
rm -f "$tmpfile"

[ -z "$MM_TOKEN" ] && die "Failed to get Mattermost admin token"
log "Admin session obtained."

# ── Step 3: Create team and get admin user info ──────────────────────────────

log "Creating team '${TEAM_NAME}'..."
mm_api POST "/teams" \
  -d "{
    \"name\": \"${TEAM_NAME}\",
    \"display_name\": \"Multi-Agent Demo\",
    \"type\": \"O\"
  }" > /dev/null 2>&1 || log "Team may already exist"

TEAM_ID=$(mm_api GET "/teams/name/${TEAM_NAME}" | jq -r '.id // empty')
[ -z "$TEAM_ID" ] && die "Could not find team '${TEAM_NAME}'"
log "Team ID: ${TEAM_ID}"

# Get default channel (town-square)
CHANNEL_ID=$(mm_api GET "/teams/${TEAM_ID}/channels/name/town-square" | jq -r '.id // empty')
[ -z "$CHANNEL_ID" ] && die "Could not find town-square channel"
log "Town-square channel: ${CHANNEL_ID}"

# Get admin user ID
ADMIN_USER_ID=$(mm_api GET "/users/me" | jq -r '.id // empty')
[ -z "$ADMIN_USER_ID" ] && die "Could not get admin user ID"

# Generate a personal access token for the bridge auto-login
log "Generating bridge auto-login token..."
ADMIN_PAT=$(mm_api POST "/users/${ADMIN_USER_ID}/tokens" \
  -d '{"description": "bridge auto-login token"}' | jq -r '.token // empty')
[ -z "$ADMIN_PAT" ] && die "Failed to generate admin access token"

# ── Step 4: Register Synapse admin user ──────────────────────────────────────

log "Registering Synapse admin user..."
NONCE=$(curl -s "${SYNAPSE_URL}/_synapse/admin/v1/register" 2>/dev/null | jq -r '.nonce // empty')
if [ -n "$NONCE" ]; then
  MAC=$(printf '%s\0%s\0%s\0%s' "$NONCE" "admin" "${MM_ADMIN_PASSWORD}" "admin" \
    | openssl dgst -sha1 -hmac "$SYNAPSE_SHARED_SECRET" 2>/dev/null \
    | awk '{print $NF}')

  REG_RESULT=$(curl -s -X POST "${SYNAPSE_URL}/_synapse/admin/v1/register" \
    -H "Content-Type: application/json" \
    -d "{
      \"nonce\": \"$NONCE\",
      \"username\": \"admin\",
      \"password\": \"${MM_ADMIN_PASSWORD}\",
      \"admin\": true,
      \"mac\": \"$MAC\"
    }" 2>/dev/null)

  if echo "$REG_RESULT" | jq -e '.user_id' > /dev/null 2>&1; then
    log "Registered @admin:${SERVER_NAME}"
  elif echo "$REG_RESULT" | jq -r '.error // empty' 2>/dev/null | grep -qi "taken"; then
    log "User @admin:${SERVER_NAME} already exists"
  else
    warn "Synapse admin registration: $(echo "$REG_RESULT" | jq -r '.error // "unknown"' 2>/dev/null)"
  fi
else
  warn "Could not get Synapse nonce"
fi

# ── Step 5: Create agent bots and puppet config ─────────────────────────────

log "Creating agent bots from ${AGENTS_CONF}..."

# Start building env.sh
cat > "$ENV_OUTPUT" << EOF
# Auto-generated by init.sh — DO NOT EDIT
# Bridge auto-login
MATTERMOST_AUTO_SERVER_URL=http://mattermost:8065
MATTERMOST_AUTO_OWNER_MXID=@admin:${SERVER_NAME}
MATTERMOST_AUTO_TOKEN=${ADMIN_PAT}

# Puppet identity mappings
EOF

AGENT_COUNT=0

while IFS=: read -r slug display_name description || [ -n "$slug" ]; do
  # Skip comments and blank lines
  [[ "$slug" =~ ^[[:space:]]*# ]] && continue
  [[ -z "$slug" ]] && continue

  slug=$(echo "$slug" | xargs)
  display_name=$(echo "$display_name" | xargs)
  description=$(echo "$description" | xargs)

  log "Creating bot: ${slug} (${display_name})..."

  # Create Mattermost bot
  mm_api POST "/bots" \
    -d "{
      \"username\": \"${slug}\",
      \"display_name\": \"${display_name}\",
      \"description\": \"${description}\"
    }" > /dev/null 2>&1 || log "  Bot ${slug} may already exist"

  # Get bot user ID
  BOT_USER_ID=$(mm_api GET "/bots" \
    | jq -r ".[] | select(.username==\"${slug}\") | .user_id // empty" 2>/dev/null)
  if [ -z "$BOT_USER_ID" ]; then
    BOT_USER_ID=$(mm_api GET "/users/username/${slug}" | jq -r '.id // empty' 2>/dev/null)
  fi

  if [ -z "$BOT_USER_ID" ]; then
    warn "Could not find bot user ID for ${slug}, skipping"
    continue
  fi

  # Generate personal access token
  BOT_TOKEN=$(mm_api POST "/users/${BOT_USER_ID}/tokens" \
    -d "{\"description\": \"${slug} puppet token\"}" | jq -r '.token // empty' 2>/dev/null)

  if [ -z "$BOT_TOKEN" ]; then
    warn "Token generation failed for ${slug}, skipping"
    continue
  fi

  # Add bot to team
  mm_api POST "/teams/${TEAM_ID}/members" \
    -d "{\"team_id\": \"${TEAM_ID}\", \"user_id\": \"${BOT_USER_ID}\"}" > /dev/null 2>&1 || true

  # Add bot to town-square
  mm_api POST "/channels/${CHANNEL_ID}/members" \
    -d "{\"user_id\": \"${BOT_USER_ID}\"}" > /dev/null 2>&1 || true

  # Register corresponding Synapse user
  MXID_LOCAL="agent-${slug}"
  NONCE=$(curl -s "${SYNAPSE_URL}/_synapse/admin/v1/register" 2>/dev/null | jq -r '.nonce // empty')
  if [ -n "$NONCE" ]; then
    MAC=$(printf '%s\0%s\0%s\0%s' "$NONCE" "$MXID_LOCAL" "agent-pass" "notadmin" \
      | openssl dgst -sha1 -hmac "$SYNAPSE_SHARED_SECRET" 2>/dev/null \
      | awk '{print $NF}')

    REG_RESULT=$(curl -s -X POST "${SYNAPSE_URL}/_synapse/admin/v1/register" \
      -H "Content-Type: application/json" \
      -d "{
        \"nonce\": \"$NONCE\",
        \"username\": \"$MXID_LOCAL\",
        \"password\": \"agent-pass\",
        \"admin\": false,
        \"mac\": \"$MAC\"
      }" 2>/dev/null)

    if echo "$REG_RESULT" | jq -e '.user_id' > /dev/null 2>&1; then
      log "  Registered @${MXID_LOCAL}:${SERVER_NAME}"
    elif echo "$REG_RESULT" | jq -r '.error // empty' 2>/dev/null | grep -qi "taken"; then
      log "  User @${MXID_LOCAL}:${SERVER_NAME} already exists"
    else
      warn "  Synapse registration: $(echo "$REG_RESULT" | jq -r '.error // "unknown"' 2>/dev/null)"
    fi
  fi

  # Write puppet env vars
  ENV_KEY=$(echo "$slug" | tr '[:lower:]-' '[:upper:]_')
  cat >> "$ENV_OUTPUT" << EOF
MATTERMOST_PUPPET_${ENV_KEY}_MXID=@${MXID_LOCAL}:${SERVER_NAME}
MATTERMOST_PUPPET_${ENV_KEY}_TOKEN=${BOT_TOKEN}
EOF

  AGENT_COUNT=$((AGENT_COUNT + 1))
  log "  Bot ${slug} ready (MM user: ${BOT_USER_ID})"

done < "$AGENTS_CONF"

# ── Done ─────────────────────────────────────────────────────────────────────

log "=========================================="
log "Init complete!"
log "  Agents provisioned: ${AGENT_COUNT}"
log "  Puppet env file:    ${ENV_OUTPUT}"
log "  MM team:            ${TEAM_NAME}"
log "  MM channel:         town-square"
log "=========================================="

log "Bridge env.sh contents:"
cat "$ENV_OUTPUT"
