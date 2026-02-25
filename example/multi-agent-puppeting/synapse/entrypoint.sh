#!/bin/bash
set -e

# Generate Synapse config on first run, then patch it for the demo
if [ ! -f /data/homeserver.yaml ]; then
  echo "[synapse-init] Generating initial config..."
  /start.py generate

  echo "[synapse-init] Patching config for multi-agent demo..."
  cat >> /data/homeserver.yaml << 'EOF'

# ── Multi-agent demo additions ──────────────────────────────────────
app_service_config_files:
  - /appservices/bridge.yaml
  - /appservices/agents.yaml

registration_shared_secret: "demo_synapse_shared_secret"
enable_registration_without_verification: true
suppress_key_server_warning: true

# Relaxed rate limits for demo
rc_message:
  per_second: 100
  burst_count: 200
rc_registration:
  per_second: 100
  burst_count: 200
rc_login:
  address:
    per_second: 100
    burst_count: 200
  account:
    per_second: 100
    burst_count: 200
  failed_attempts:
    per_second: 100
    burst_count: 200
EOF

  echo "[synapse-init] Config patched successfully"
fi

exec /start.py run
