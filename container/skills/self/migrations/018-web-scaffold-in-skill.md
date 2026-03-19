# 018 — web template moved into web skill

The web app template (`package.json`, `vite.config.ts`, `pub/`, `priv/`,
`secret/`) now lives at
`/workspace/self/container/skills/web/template/` instead of
`prototype/web/`.

To scaffold a fresh web directory:

```bash
cp -rn /workspace/self/container/skills/web/template/. /workspace/web/
cd /workspace/web && npm install --silent
```
