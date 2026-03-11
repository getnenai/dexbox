# dexbox — Desktop container
#
# Runs the parent service (FastAPI), X11 desktop environment, and supporting
# services (VNC, FFmpeg, screen recording) inside a Docker container.
#
# 1. Multi-stage build for size optimization.
# 2. Uses mutter + lxpanel + chromium.
# 3. Dedicated system Python for OS-level integration.

ARG BASE_IMAGE=debian:trixie
ARG USERNAME=dexbox
ARG UID=1000
ARG GID=1000
ARG TZ=Etc/UTC
ARG PYTHON_VERSION=3.13
ARG LOCALE=en_US.UTF-8
ARG DISPLAY_NUM=1
ARG WIDTH=1024
ARG HEIGHT=768


# ============================================================================
# Stage 1: System base with OS dependencies
# ============================================================================
FROM ${BASE_IMAGE} AS system-base
ARG USERNAME UID GID TZ LOCALE DISPLAY_NUM WIDTH HEIGHT

# Setup timezone and locale
RUN apt-get update && \
    apt-get install -y --no-install-recommends tzdata locales && \
    ln -sf /usr/share/zoneinfo/${TZ} /etc/localtime && \
    echo "${TZ}" > /etc/timezone && \
    echo "${LOCALE} UTF-8" > /etc/locale.gen && \
    locale-gen && \
    apt-get clean && \
    rm -rf /var/lib/apt/lists/*

ENV LANG=${LOCALE} \
    LANGUAGE=${LOCALE%%.*}:${LOCALE%%_*} \
    LC_ALL=${LOCALE} \
    TZ=${TZ} \
    DEBIAN_FRONTEND=noninteractive \
    PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    DISPLAY=:${DISPLAY_NUM} \
    DISPLAY_NUM=${DISPLAY_NUM} \
    WIDTH=${WIDTH} \
    HEIGHT=${HEIGHT} \
    VNC_PORT=5900 \
    NOVNC_PORT=6080 \
    HOME=/home/${USERNAME}

# Install OS-level packages (changes occasionally)
RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends \
    # X11 / Desktop
    xvfb tigervnc-scraping-server tigervnc-common mutter lxpanel pcmanfm xterm mousepad \
    xdotool scrot imagemagick gpicview \
    # System Python bindings (Essential for OS-level scripting, cannot be in Venv)
    python3 python3-apt python3-gi python3-lazr.restfulclient \
    dbus-x11 \
    # Utils
    git curl ca-certificates net-tools unzip \
    gpg iso-codes lsb-release wget file gnupg \
    supervisor ffmpeg socat \
    # Diagnostic tools
    iproute2 x11-xserver-utils \
    # Desktop applications
    chromium gedit \
    # RDP client
    freerdp3-x11 \
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/* \
    # Compatibility symlink for xfreerdp -> xfreerdp3
    && ln -s /usr/bin/xfreerdp3 /usr/bin/xfreerdp

# Clone noVNC
RUN git clone --depth 1 --branch v1.6.0 https://github.com/novnc/noVNC.git /opt/noVNC && \
    git clone --depth 1 --branch v0.13.0 https://github.com/novnc/websockify /opt/noVNC/utils/websockify && \
    ln -s /opt/noVNC/vnc.html /opt/noVNC/index.html

# Create the non-root user
RUN groupadd -f -g "${GID}" "${USERNAME}" && \
    useradd -m -o -u "${UID}" -g "${GID}" -s /bin/bash "${USERNAME}" && \
    groupadd -f docker && \
    usermod -aG docker "${USERNAME}" && \
    mkdir -p /tmp/runtime-user /artifacts /mnt/tmp && \
    chown ${UID}:${GID} /tmp/runtime-user /artifacts /mnt/tmp && \
    chmod 700 /tmp/runtime-user



# ============================================================================
# Stage 3: Go builder (for dexbox CLI and server)
# ============================================================================
FROM golang:1.26 AS go-builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
COPY pkg/ pkg/
RUN CGO_ENABLED=0 go build -o /usr/local/bin/dexbox ./cmd/dexbox

# ============================================================================
# Stage 3: Final Runtime Image
# ============================================================================
FROM system-base AS runtime

# Copy Go binary
COPY --from=go-builder /usr/local/bin/dexbox /usr/local/bin/dexbox

# Supervisor + entrypoint config
COPY docker/supervisord.conf /etc/supervisor/supervisord.conf
COPY docker/services/ /etc/supervisor/conf.d/
COPY docker/bin/ /app/docker/bin/
COPY docker/config/lxpanel /home/${USERNAME}/.config/lxpanel
COPY docker/config/gtk-3.0 /home/${USERNAME}/.config/gtk-3.0
COPY docker/config/libfm /home/${USERNAME}/.config/libfm

# Fix ownership and permissions
RUN chown -R ${UID}:${GID} /home/${USERNAME} && \
    chmod +x /app/docker/bin/entrypoint.sh

WORKDIR /app

EXPOSE 8600 5900 6080 5001

ENTRYPOINT ["/app/docker/bin/entrypoint.sh"]
