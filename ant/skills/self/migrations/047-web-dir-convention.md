# 047 — web dir convention

The `/workspace/web` mount is already group-scoped by the gateway.
Writing to `/workspace/web/$ARIZUKO_GROUP_FOLDER/` nests content one
level too deep and hides it from the web mount. Flatten if present:

```bash
GROUP_SUB=$(basename "$ARIZUKO_GROUP_FOLDER")
if [ -d "/workspace/web/$GROUP_SUB" ]; then
  mv /workspace/web/$GROUP_SUB/* /workspace/web/ 2>/dev/null \
    && rmdir /workspace/web/$GROUP_SUB
fi
```
