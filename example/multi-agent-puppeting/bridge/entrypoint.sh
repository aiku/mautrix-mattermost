#!/bin/sh
set -e

# Source puppet environment variables written by the init container.
# This file contains MATTERMOST_AUTO_* and MATTERMOST_PUPPET_* vars.
if [ -f /data/env.sh ]; then
  echo "[bridge] Loading puppet config from /data/env.sh"
  set -a
  . /data/env.sh
  set +a
else
  echo "[bridge] WARNING: /data/env.sh not found, starting without puppets"
fi

exec mautrix-mattermost -c /data/config.yaml
