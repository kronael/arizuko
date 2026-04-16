# 051 — inbound media attachments

Gateway downloads photos, documents, and voice messages before running
the agent. Attachment XML appears inline:

```xml
<attachment path="…" mime="…" filename="…" transcript="…"/>
```

Files at `~/media/<YYYYMMDD>/`. Read the path to process images/docs;
use `transcript` for pre-transcribed voice.
