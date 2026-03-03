#!/bin/bash
# =============================================================================
# dexbox Entrypoint
# =============================================================================
# Runs as root to fix Docker socket GID at runtime, then supervisord
# launches all services as the 'dexbox' non-root user.
# =============================================================================

set -e  # Exit on any error

# -----------------------------------------------------------------------------
# Fix Docker socket GID at runtime
# -----------------------------------------------------------------------------
# Problem: The docker group GID varies across hosts (EC2: 989, Mac: ???, etc).
# Build-time hardcoding breaks when image is built on one host and run on another.
#
# Solution: Detect the mounted socket's actual GID at container startup and
# ensure dexbox belongs to a group with that GID.

if [ -S /var/run/docker.sock ]; then
    # Get the actual GID of the mounted socket (from host)
    SOCKET_GID=$(stat -c '%g' /var/run/docker.sock 2>/dev/null || stat -f '%g' /var/run/docker.sock 2>/dev/null)

    if [ -n "$SOCKET_GID" ]; then
        echo "[Entrypoint] Configuring Docker socket access (host GID: ${SOCKET_GID})..."

        # Check if ANY group in the container already has this GID
        EXISTING_GROUP=$(getent group "$SOCKET_GID" | cut -d: -f1 2>/dev/null || echo "")

        if [ -n "$EXISTING_GROUP" ]; then
            # A container group already has this GID - use it
            if ! id -nG dexbox | grep -qw "$EXISTING_GROUP"; then
                echo "[Entrypoint] Adding 'dexbox' to existing group '${EXISTING_GROUP}' (GID ${SOCKET_GID})"
                usermod -aG "$EXISTING_GROUP" dexbox
            fi
        else
            # No container group has this GID - create or modify one
            if getent group docker >/dev/null 2>&1; then
                echo "[Entrypoint] Changing 'docker' group GID to ${SOCKET_GID}"
                groupmod -g "${SOCKET_GID}" docker 2>/dev/null || {
                    echo "[Entrypoint] WARN: groupmod failed, creating docker-socket group instead"
                    groupadd -g "${SOCKET_GID}" docker-socket 2>/dev/null
                    usermod -aG docker-socket dexbox
                }
            else
                groupadd -g "${SOCKET_GID}" docker 2>/dev/null || {
                    echo "[Entrypoint] WARN: groupadd failed"
                }
            fi
            usermod -aG docker dexbox 2>/dev/null || true
        fi

        # Verify the user now has the socket's GID in their groups
        USER_GIDS=$(id -G dexbox)
        if echo "$USER_GIDS" | grep -qw "$SOCKET_GID"; then
            echo "[Entrypoint] Docker socket configured successfully"
        else
            echo "[Entrypoint] ERROR: Socket access configuration failed - user missing GID ${SOCKET_GID}"
            echo "[Entrypoint] ERROR: User GIDs: ${USER_GIDS} | Groups: $(id -nG dexbox)"
        fi

        # Ensure socket has group read/write permissions
        chmod g+rw /var/run/docker.sock 2>/dev/null || echo "[Entrypoint] WARN: Could not chmod socket"
    else
        echo "[Entrypoint] WARNING: Could not determine socket GID"
    fi
else
    echo "[Entrypoint] ERROR: No Docker socket mounted (FATAL: all workflow will fail)"
fi

# Create runtime directories needed by mutter and desktop services
mkdir -p /tmp/recordings /tmp/outputs /tmp/dexbox-artifacts
mkdir -p /var/run/supervisor /var/log/supervisor
chown dexbox:dexbox /tmp/recordings /tmp/outputs /tmp/dexbox-artifacts

# -----------------------------------------------------------------------------
# Start supervisord or exec provided command
# -----------------------------------------------------------------------------
if [ $# -gt 0 ]; then
    echo "[Entrypoint] Executing command: $@"
    exec "$@"
else
    echo "[Entrypoint] Starting supervisord..."
    exec /usr/bin/supervisord -n -c /etc/supervisor/supervisord.conf
fi
