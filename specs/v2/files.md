# File Handling

Inbound: user uploads files to agent workspace.
Outbound: agent writes files, gateway sends them back.

## Inbound (user → agent)

### Detection

| Field          | Notes                              |
| -------------- | ---------------------------------- |
| `msg.document` | any file                           |
| `msg.photo`    | array — use last element (largest) |
| `msg.video`    | video files                        |

### Download

grammy file API → save to:

```
groups/<folder>/uploads/<timestamp>_<original_filename>
```

`timestamp` = Unix ms. `original_filename` from `file_name` field; fallback
to `photo_<id>.jpg` or `video_<id>.mp4` when absent.

Reject files >50 MB before download: reply `"file too large (max 50 MB)"`.

### Injection

Prepend path to agent message (caption or empty string):

```
[file: /workspace/group/uploads/<timestamp>_<filename>]
<caption>
```

Agent sees files under `/workspace/group/uploads/` — the group folder mounts
to `/workspace/group` (see `container-runner.ts:buildVolumeMounts`).

## Outbound (agent → user)

### IPC message

Agent writes file to `/workspace/group/output/<filename>` then emits to
`/workspace/ipc/messages/<file>.json`:

```json
{ "type": "send_file", "path": "output/foo.png" }
```

`path` is relative to `/workspace/group/` (the group folder on host).

`send_file` is a new type — add handling alongside the existing `message`
type in `ipc.ts:processIpcFiles`. Resolve absolute host path via `hostPath()`
(same translation used for session dirs).

### Gateway handling

On receiving `send_file`:

```
ctx.replyWithDocument({ source: fs.createReadStream(absPath) })
```

File is sent to the same chat the agent is responding in.
Catch send errors for files >50 MB and reply with an error message.

## Notes

- `uploads/` and `output/` dirs created on first use
- Multiple files in one message: handle first file only (v2.0)
- No cleanup policy — operator responsibility
