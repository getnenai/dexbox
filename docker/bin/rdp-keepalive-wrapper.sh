#!/bin/bash
#
# RDP Keepalive Wrapper
#
# This script checks if RDP keepalive should be enabled based on the DEXBOX_BACKEND
# environment variable. If enabled, it launches xfreerdp with all settings passed
# as command-line parameters (no .rdp file needed).
#
# Environment Variables:
#   DEXBOX_BACKEND             - Set to "rdp" to enable RDP keepalive
#   RDP_HOST                   - RDP server hostname (e.g., "hostname.domain.local")
#   RDP_USERNAME               - RDP username (e.g., "name@domain.local")
#   RDP_PASSWORD               - RDP connection password
#   RDP_SECURITY               - Security protocol: "rdp", "tls", "nla", or "" for auto (default: auto)
#   RDP_RETRY_DELAY_SECONDS    - Seconds to wait between retry attempts (default: 60)
#                                Helps avoid overwhelming server with rapid reconnects
#

set -euo pipefail

# Log function - output to stdout
log() {
    echo "[$(date +'%Y-%m-%d %H:%M:%S')] $*"
}

# Error log function - output to stderr
log_error() {
    echo "[$(date +'%Y-%m-%d %H:%M:%S')] ERROR: $*" >&2
}

# Wait until DISPLAY is available
until xset -display :${DISPLAY_NUM:-1} q > /dev/null 2>&1; do sleep 0.1; done;

if [[ "${DEXBOX_BACKEND:-rdp}" != "rdp" ]]; then
    log "RDP keepalive disabled (DEXBOX_BACKEND != 'rdp')"
    log "Sleeping indefinitely to keep supervisor happy..."
    # Sleep forever - supervisor will not restart since this is a successful exit
    exec sleep infinity
fi

# Validate required environment variables
if [[ -z "${RDP_HOST:-}" ]]; then
    log_error "RDP_HOST not set - cannot launch RDP without host. Sleeping indefinitely."
    exec sleep infinity
fi

if [[ -z "${RDP_USERNAME:-}" ]]; then
    log_error "RDP_USERNAME not set - cannot launch RDP without username. Sleeping indefinitely."
    exec sleep infinity
fi

if [[ -z "${RDP_PASSWORD:-}" ]]; then
    log_error "RDP_PASSWORD not set - cannot launch RDP without password. Sleeping indefinitely."
    exec sleep infinity
fi

log "RDP keepalive ENABLED"
log "DEXBOX_BACKEND: ${DEXBOX_BACKEND}"
log "RDP_HOST: ${RDP_HOST}"
log "RDP_USERNAME: ${RDP_USERNAME}"
log "RDP_SECURITY: ${RDP_SECURITY:-auto}"

# Check for retry delay (to avoid overwhelming the server)
RETRY_DELAY="${RDP_RETRY_DELAY_SECONDS:-60}"
LAST_ATTEMPT_FILE="/tmp/rdp-keepalive-last-attempt"

# Check when we last attempted a connection
if [[ -f "$LAST_ATTEMPT_FILE" ]]; then
    LAST_ATTEMPT=$(cat "$LAST_ATTEMPT_FILE")
    CURRENT_TIME=$(date +%s)
    TIME_SINCE_LAST=$((CURRENT_TIME - LAST_ATTEMPT))

    # If we attempted recently, wait before trying again
    if [[ $TIME_SINCE_LAST -lt $RETRY_DELAY ]]; then
        WAIT_TIME=$((RETRY_DELAY - TIME_SINCE_LAST))
        log "Last connection attempt was ${TIME_SINCE_LAST}s ago. Waiting ${WAIT_TIME}s before retry..."
        sleep "$WAIT_TIME"
    else
        log "Last attempt was ${TIME_SINCE_LAST}s ago (> ${RETRY_DELAY}s threshold), proceeding immediately"
    fi
fi

# Record this attempt
date +%s > "$LAST_ATTEMPT_FILE"

log "Launching xfreerdp with password from RDP_PASSWORD environment variable"

# Determine security protocol argument
RDP_SECURITY="${RDP_SECURITY:-}"
if [[ -n "${RDP_SECURITY}" ]]; then
    SECURITY_ARG="/sec:${RDP_SECURITY}"
    log "Using configured security protocol: ${RDP_SECURITY}"
else
    SECURITY_ARG=""
    log "Using auto-negotiated security protocol"
fi

log "Using production-tested connection parameters: /cert:tofu"
log "TESTING: Increased activation timeout to 30 seconds (/timeout:30000)"
log "Retry delay configured: ${RETRY_DELAY}s minimum between connection attempts"

# Launch xfreerdp with production-tested parameters
# Connection & Security:
#   /v: - Server hostname/address
#   /u: - Username
#   /p: - Password
#   /sec: - Security protocol (optional: rdp, tls, nla, or omit for auto)
#   /cert:tofu - Trust On First Use for certificate validation
#   /auto-reconnect - Automatically reconnect if connection drops
#   /auto-reconnect-max-retries:5 - Limit reconnection attempts
#   /network:auto - Network auto-detection
#
# Display:
#   +f - Fullscreen mode
#   -decorations - Remove window decorations (no title bar)
#   -toggle-fullscreen - Disable Ctrl+Alt+Enter toggle (keeps session locked in fullscreen)
#   +dynamic-resolution - Send resolution updates when window size changes
#   /bpp:32 - 32-bit color depth
#
# Performance:
#   +compression - Enable RDP compression
#   -wallpaper - Disable wallpaper
#   +window-drag - Enable full window drag
#   +aero - Enable desktop composition
#
# Redirects:
#   +clipboard - Enable clipboard sharing
#   /drive:tmp,/mnt/tmp - Mount tmp directory
#   /audio-mode:0 - Disable audio redirection (0 = no audio)
#   (printer and smartcard disabled by not including them)
#
# This will block until xfreerdp exits
# If it exits, supervisor will restart it (autorestart=true)
# Retry delay is handled at script start based on last attempt timestamp
exec xfreerdp \
    /v:"${RDP_HOST}" \
    /u:"${RDP_USERNAME}" \
    /p:"${RDP_PASSWORD}" \
    ${SECURITY_ARG} \
    /cert:tofu \
    /auto-reconnect \
    /auto-reconnect-max-retries:5 \
    /network:auto \
    /timeout:30000 \
    +f \
    -decorations \
    -toggle-fullscreen \
    +dynamic-resolution \
    /bpp:32 \
    +compression \
    -wallpaper \
    +window-drag \
    +aero \
    +clipboard \
    /drive:tmp,/mnt/tmp \
    /audio-mode:0

