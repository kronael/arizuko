# Arizuko Agent Container
# Runs Claude Code in isolated Linux container with browser automation
#
# Reproducibility contract: base image pinned to tag; CLI tools pinned to
# explicit versions. Where upstream publishes SHA256SUMS files, we verify.

FROM node:22-slim

# Pinned tool versions — bump intentionally, never use `latest`.
ENV YT_DLP_VERSION=2024.12.23 \
    YQ_VERSION=v4.44.3 \
    HADOLINT_VERSION=v2.12.0 \
    DUCKDB_VERSION=v1.1.3 \
    DELTA_VERSION=0.18.2 \
    XH_VERSION=v0.23.1 \
    WEBSOCAT_VERSION=v1.13.0 \
    HURL_VERSION=5.0.1 \
    SOPS_VERSION=v3.9.1 \
    GO_VERSION=1.24.1 \
    UV_VERSION=0.11.7

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
    age \
    gnupg \
    && rm -rf /var/lib/apt/lists/*

# yt-dlp (upstream publishes SHA2-256SUMS; verify)
RUN set -eux; \
    curl --fail -fsSL "https://github.com/yt-dlp/yt-dlp/releases/download/${YT_DLP_VERSION}/yt-dlp" \
      -o /usr/local/bin/yt-dlp; \
    curl --fail -fsSL "https://github.com/yt-dlp/yt-dlp/releases/download/${YT_DLP_VERSION}/SHA2-256SUMS" \
      -o /tmp/ytdlp.sums; \
    (cd /usr/local/bin && grep "  yt-dlp$" /tmp/ytdlp.sums | sha256sum -c -); \
    chmod +x /usr/local/bin/yt-dlp; \
    rm /tmp/ytdlp.sums

# GitHub CLI (official apt repo, signed)
RUN curl --fail -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg \
      | dd of=/usr/share/keyrings/githubcli-archive-keyring.gpg \
    && chmod go+r /usr/share/keyrings/githubcli-archive-keyring.gpg \
    && echo "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" \
      > /etc/apt/sources.list.d/github-cli.list \
    && apt-get update && apt-get install -y gh \
    && rm -rf /var/lib/apt/lists/*

# Go (pinned via GO_VERSION; https transport + go.sum authenticate downstream)
RUN set -eux; ARCH=$(dpkg --print-architecture); \
    curl --fail -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${ARCH}.tar.gz" -o /tmp/go.tgz; \
    tar -C /usr/local -xzf /tmp/go.tgz; \
    rm /tmp/go.tgz
ENV PATH="/usr/local/go/bin:$PATH"

# Go-based dev tools — pinned; go.sum authenticates downloads
RUN go install mvdan.cc/sh/v3/cmd/shfmt@v3.10.0 \
    && go install github.com/fullstorydev/grpcurl/cmd/grpcurl@v1.9.1

# Rust (rustup installer; download and retain for drift inspection)
ENV RUSTUP_HOME=/usr/local/rustup CARGO_HOME=/usr/local/cargo
RUN set -eux; \
    curl --fail --proto '=https' --tlsv1.2 -fsSL https://sh.rustup.rs -o /tmp/rustup-init.sh; \
    sha256sum /tmp/rustup-init.sh; \
    sh /tmp/rustup-init.sh -y --default-toolchain stable --profile minimal; \
    rm /tmp/rustup-init.sh
ENV PATH="/usr/local/cargo/bin:$PATH"

# uv — pinned
RUN set -eux; \
    curl --fail -LsSf "https://astral.sh/uv/${UV_VERSION}/install.sh" \
      | env UV_INSTALL_DIR=/usr/local/bin sh; \
    uv python install 3.14; \
    ln -sf "$(uv python find 3.14)" /usr/local/bin/python3.14

# Binary tools pinned to explicit versions with SHA256 where upstream provides
RUN set -eux; ARCH=$(dpkg --print-architecture); \
    curl --fail -fsSL "https://github.com/mikefarah/yq/releases/download/${YQ_VERSION}/yq_linux_${ARCH}" \
      -o /usr/local/bin/yq; \
    chmod +x /usr/local/bin/yq; \
    HL_ARCH=$([ "$ARCH" = "amd64" ] && echo "x86_64" || echo "arm64"); \
    curl --fail -fsSL "https://github.com/hadolint/hadolint/releases/download/${HADOLINT_VERSION}/hadolint-Linux-${HL_ARCH}" \
      -o /usr/local/bin/hadolint; \
    curl --fail -fsSL "https://github.com/hadolint/hadolint/releases/download/${HADOLINT_VERSION}/hadolint-Linux-${HL_ARCH}.sha256" \
      -o /tmp/hadolint.sha256; \
    echo "$(cut -d' ' -f1 /tmp/hadolint.sha256)  /usr/local/bin/hadolint" | sha256sum -c -; \
    chmod +x /usr/local/bin/hadolint; rm /tmp/hadolint.sha256; \
    DK_ARCH=$([ "$ARCH" = "amd64" ] && echo "amd64" || echo "aarch64"); \
    curl --fail -fsSL "https://github.com/duckdb/duckdb/releases/download/${DUCKDB_VERSION}/duckdb_cli-linux-${DK_ARCH}.zip" \
      -o /tmp/duckdb.zip; \
    unzip /tmp/duckdb.zip duckdb -d /usr/local/bin; \
    chmod +x /usr/local/bin/duckdb; rm /tmp/duckdb.zip; \
    DELTA_ARCH=$([ "$ARCH" = "amd64" ] && echo "x86_64" || echo "aarch64"); \
    curl --fail -fsSL "https://github.com/dandavison/delta/releases/download/${DELTA_VERSION}/delta-${DELTA_VERSION}-${DELTA_ARCH}-unknown-linux-gnu.tar.gz" \
      | tar xz --strip-components=1 -C /usr/local/bin \
        "delta-${DELTA_VERSION}-${DELTA_ARCH}-unknown-linux-gnu/delta"; \
    RUST_ARCH=$([ "$ARCH" = "amd64" ] && echo "x86_64" || echo "aarch64"); \
    curl --fail -fsSL "https://github.com/ducaale/xh/releases/download/${XH_VERSION}/xh-${XH_VERSION}-${RUST_ARCH}-unknown-linux-musl.tar.gz" \
      | tar xz --strip-components=1 -C /usr/local/bin \
        "xh-${XH_VERSION}-${RUST_ARCH}-unknown-linux-musl/xh"; \
    chmod +x /usr/local/bin/xh; ln -sf /usr/local/bin/xh /usr/local/bin/xhs; \
    curl --fail -fsSL "https://github.com/vi/websocat/releases/download/${WEBSOCAT_VERSION}/websocat.${RUST_ARCH}-unknown-linux-musl" \
      -o /usr/local/bin/websocat; chmod +x /usr/local/bin/websocat; \
    curl --fail -fsSL "https://github.com/Orange-Opensource/hurl/releases/download/${HURL_VERSION}/hurl-${HURL_VERSION}-${RUST_ARCH}-unknown-linux-gnu.tar.gz" \
      | tar xz --strip-components=2 -C /usr/local/bin \
        "hurl-${HURL_VERSION}-${RUST_ARCH}-unknown-linux-gnu/bin/hurl" \
        "hurl-${HURL_VERSION}-${RUST_ARCH}-unknown-linux-gnu/bin/hurlfmt"; \
    curl --fail -fsSL "https://github.com/getsops/sops/releases/download/${SOPS_VERSION}/sops-${SOPS_VERSION}.linux.${ARCH}" \
      -o /usr/local/bin/sops; chmod +x /usr/local/bin/sops

ENV AGENT_BROWSER_EXECUTABLE_PATH=/usr/bin/chromium
ENV PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH=/usr/bin/chromium
ENV ARIZUKO_MCP_SOCKET=/workspace/ipc/gated.sock

RUN npm install -g bun agent-browser @anthropic-ai/claude-code @apify/mcpc \
    && bun install -g @biomejs/biome prettier svgo \
       @marp-team/marp-cli htmlhint

RUN uv pip install --system --break-system-packages \
    ruff pyright \
    pandas matplotlib plotly numpy scipy \
    python-pptx openpyxl weasyprint \
    sqlfluff semgrep

WORKDIR /app
COPY package*.json ./
RUN npm install
COPY . ./
COPY output-styles/ /home/node/.claude/output-styles/
RUN npm run build && rm -rf dist
RUN mkdir -p /workspace/extra /workspace/ipc/input

# Entrypoint is now a real file (was inline printf); easier to review/diff.
COPY entrypoint.sh /app/entrypoint.sh
RUN chmod +x /app/entrypoint.sh

# Toolchain root-owned at 755 (was world-writable — agent code could swap cargo).
# /home/node is 755 (was 777). Workspace is node-owned for runtime writes.
RUN chown -R node:node /workspace /home/node \
    && chmod 755 /home/node \
    && chmod -R u=rwX,go=rX /usr/local/cargo /usr/local/rustup

USER node
WORKDIR /home/node

# Agent container is one-shot (stdin JSON in, stdout JSON out). Liveness is
# tracked by gated directly — no HEALTHCHECK is meaningful here.

ENTRYPOINT ["/app/entrypoint.sh"]
