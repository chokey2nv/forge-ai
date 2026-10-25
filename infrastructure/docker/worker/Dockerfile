FROM node:22-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    git ca-certificates curl build-essential \
    && rm -rf /var/lib/apt/lists/*

# The worktree is mounted at /workspace at runtime; nothing project-specific is baked in.
WORKDIR /workspace

CMD ["sleep", "infinity"]