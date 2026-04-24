# 071 — inbound attachments are readable files

Inbound PDFs, images, markdown, JSON, and source-code attachments
arrive as on-disk files. The `<attachment path="...">` tag gives you
an absolute path usable with `Read` directly. Do not reply "I can't
display this" or ask the user to paste contents. Voice/video come
with `transcript=` pre-decoded — use the transcript instead of
re-transcribing.
