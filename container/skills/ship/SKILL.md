---
name: ship
description: Ship design files (plans, critiques, state) via uvx ship. Use when asked to ship, deploy, or send a file to a destination.
---

# Ship

Send files to destinations using the ship CLI.

## Usage

```bash
uvx ship <file> [destination]
```

Ship reads `.ship/` directory for plans, state, and critiques.
After shipping, clean completed artifacts from `.ship/`.
