# NanoClaw Agent Container
# Runs Claude Agent SDK in isolated Linux VM with browser automation

FROM node:22-slim

# Install system dependencies for Chromium
RUN apt-get update && apt-get install -y \
    chromium \
    fonts-liberation \
    fonts-noto-color-emoji \
    libgbm1 \
    libnss3 \
    libatk-bridge2.0-0 \
    libgtk-3-0 \
    libx11-xcb1 \
    libxcomposite1 \
    libxdamage1 \
    libxrandr2 \
    libasound2 \
    libpangocairo-1.0-0 \
    libcups2 \
    libdrm2 \
    libxshmfence1 \
    curl \
    wget \
    git \
    jq \
    ffmpeg \
    python3 \
    python3-pip \
    build-essential \
    pkg-config \
    httrack \
    traceroute \
    dnsutils \
    net-tools \
    whois \
    shellcheck \
    ripgrep \
    fd-find \
    tree \
    bat \
    fzf \
    optipng \
    jpegoptim \
    imagemagick \
    pandoc \
    poppler-utils \
    tesseract-ocr \
    sqlite3 \
    postgresql-client \
    redis-tools \
    socat \
    graphviz \
    ghostscript \
    sox \
    mediainfo \
    qrencode \
    parallel \
    rsync \
    sysstat \
    libimage-exiftool-perl \
    miller \
    unzip \
    && rm -rf /var/lib/apt/lists/* \
    && curl -L https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp \
       -o /usr/local/bin/yt-dlp && chmod +x /usr/local/bin/yt-dlp

# GitHub CLI
RUN curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg \
      | dd of=/usr/share/keyrings/githubcli-archive-keyring.gpg \
    && chmod go+r /usr/share/keyrings/githubcli-archive-keyring.gpg \
    && echo "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" \
      > /etc/apt/sources.list.d/github-cli.list \
    && apt-get update && apt-get install -y gh \
    && rm -rf /var/lib/apt/lists/*

# Go (agent can fetch+compile Go tools)
RUN curl -fsSL https://go.dev/dl/go1.24.1.linux-$(dpkg --print-architecture).tar.gz \
    | tar -C /usr/local -xz
ENV PATH="/usr/local/go/bin:$PATH"

# Go-based dev tools
RUN go install mvdan.cc/sh/v3/cmd/shfmt@latest \
    && go install github.com/fullstorydev/grpcurl/cmd/grpcurl@latest

# Rust (agent can cargo install tools)
ENV RUSTUP_HOME=/usr/local/rustup CARGO_HOME=/usr/local/cargo
RUN curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs \
    | sh -s -- -y --default-toolchain stable --profile minimal \
    && chmod -R a+rw /usr/local/cargo /usr/local/rustup
ENV PATH="/usr/local/cargo/bin:$PATH"

# uv — fast Python package manager
RUN curl -LsSf https://astral.sh/uv/install.sh | env UV_INSTALL_DIR=/usr/local/bin sh

# Binary dev tools (yq, duckdb, hadolint, delta)
RUN set -e; ARCH=$(dpkg --print-architecture); \
    curl -fsSL "https://github.com/mikefarah/yq/releases/latest/download/yq_linux_${ARCH}" \
      -o /usr/local/bin/yq && chmod +x /usr/local/bin/yq; \
    HL_ARCH=$([ "$ARCH" = "amd64" ] && echo "x86_64" || echo "arm64"); \
    curl -fsSL "https://github.com/hadolint/hadolint/releases/latest/download/hadolint-Linux-${HL_ARCH}" \
      -o /usr/local/bin/hadolint && chmod +x /usr/local/bin/hadolint; \
    DK_ARCH=$([ "$ARCH" = "amd64" ] && echo "amd64" || echo "aarch64"); \
    curl -fsSL "https://github.com/duckdb/duckdb/releases/latest/download/duckdb_cli-linux-${DK_ARCH}.zip" \
      -o /tmp/duckdb.zip && unzip /tmp/duckdb.zip duckdb -d /usr/local/bin \
      && chmod +x /usr/local/bin/duckdb && rm /tmp/duckdb.zip; \
    DELTA_VER=$(curl -fsSL https://api.github.com/repos/dandavison/delta/releases/latest | jq -r .tag_name); \
    DELTA_ARCH=$([ "$ARCH" = "amd64" ] && echo "x86_64" || echo "aarch64"); \
    curl -fsSL "https://github.com/dandavison/delta/releases/download/${DELTA_VER}/delta-${DELTA_VER}-${DELTA_ARCH}-unknown-linux-gnu.tar.gz" \
      | tar xz --strip-components=1 -C /usr/local/bin \
        "delta-${DELTA_VER}-${DELTA_ARCH}-unknown-linux-gnu/delta"

# Set Chromium path for agent-browser
ENV AGENT_BROWSER_EXECUTABLE_PATH=/usr/bin/chromium
ENV PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH=/usr/bin/chromium

# Install bun, agent-browser and claude-code globally
RUN npm install -g bun agent-browser @anthropic-ai/claude-code \
    && bun install -g @biomejs/biome prettier svgo \
       @marp-team/marp-cli htmlhint

# Python tooling (break-system-packages: container-only, no venv needed)
RUN uv pip install --system --break-system-packages \
    ruff pyright \
    pandas matplotlib plotly numpy scipy \
    python-pptx openpyxl weasyprint \
    sqlfluff semgrep

# Create app directory
WORKDIR /app

# Copy package files first for better caching
COPY agent-runner/package*.json ./

# Install dependencies
RUN npm install

# Copy source code
COPY agent-runner/ ./

# Bake per-channel output styles into agent image
COPY output-styles/ /home/node/.claude/output-styles/

# Validate TypeScript compiles (catches errors at build time)
RUN npm run build && rm -rf dist

# Create workspace directories
RUN mkdir -p /workspace/group /workspace/global /workspace/extra /workspace/ipc/messages /workspace/ipc/tasks /workspace/ipc/input

# Create entrypoint script
# Secrets are passed via stdin JSON — temp file is deleted immediately after Node reads it
# Follow-up messages arrive via IPC files in /workspace/ipc/input/
RUN printf '#!/bin/bash\nset -e\ncd /app && bunx tsc --outDir /tmp/dist 2>&1 >&2\nln -s /app/node_modules /tmp/dist/node_modules\nchmod -R a-w /tmp/dist\ncat > /tmp/input.json\nnode /tmp/dist/index.js < /tmp/input.json\n' > /app/entrypoint.sh && chmod +x /app/entrypoint.sh

# Set ownership to node user (non-root) for writable directories
RUN chown -R node:node /workspace && chmod 777 /home/node

# Switch to non-root user (required for --dangerously-skip-permissions)
USER node

# Set working directory to group workspace
WORKDIR /workspace/group

# Entry point reads JSON from stdin, outputs JSON to stdout
ENTRYPOINT ["/app/entrypoint.sh"]
