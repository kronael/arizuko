#!/usr/bin/env python3
"""The one group config of the digest: `$DIGEST_CONFIG`, default `~/digest.toml`.

usage: config.py repo | clone | post_jid | agent_github | agent_folder | channels
Python scripts `import config` and call `load()`.
"""
import os
import sys
import tomllib

HOME = os.environ.get("HOME", "/home/node")
PATH = os.environ.get("DIGEST_CONFIG", os.path.join(HOME, "digest.toml"))


def load():
    try:
        with open(PATH, "rb") as f:
            return tomllib.load(f)
    except FileNotFoundError:
        sys.exit(f"digest: no config at {PATH}; see skills/digest/SKILL.md")


def channels(cfg):
    return [(c["jid"], c["name"], c.get("signal", "medium")) for c in cfg["channels"]]


def main():
    cfg = load()
    key = sys.argv[1]
    if key == "channels":
        for jid, name, sig in channels(cfg):
            print(f"{jid}|{name}|{sig}")
    else:
        print(cfg[key])


if __name__ == "__main__":
    main()
