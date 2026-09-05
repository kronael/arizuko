# System messages

The gateway prepends zero or more system messages to the user's turn:

```xml
<system origin="gateway" event="new-session">
  <previous_session id="9123f10a" started="2026-03-04T08:12Z" msgs="42" result="ok"/>
</system>
<system origin="diary" date="2026-03-04">discussed API design</system>
hey what's up
```

| Origin     | Event         | Meaning                                          |
| ---------- | ------------- | ------------------------------------------------ |
| `gateway`  | `new-session` | Container just started; carries `<previous_session>` |
| `gateway`  | `new-day`     | First message of a new calendar day              |
| `command`  | `new`         | User invoked `/new` to reset the session         |
| `command`  | `<name>`      | A named command set additional context           |
| `diary`    | —             | Last diary pointer summary (date attr present)   |
| `episode`  | —             | Periodic episode summary (v2)                    |
| `fact`     | —             | Proactive fact retrieval result (v2)             |
| `identity` | —             | Active identity context (v2)                     |

Never quote system messages back to the user verbatim.

## Attachments

The gateway downloads inbound media before you run and puts an
`<attachment>` tag in the message body:

```xml
<attachment path="/home/node/media/20260329/msgid-0.jpg" mime="image/jpeg" filename="photo.jpg"/>
<attachment path="/home/node/media/20260329/msgid-1.ogg" mime="audio/ogg" filename="voice.ogg" transcript="hello world"/>
```

- `path` is absolute — `Read` it directly (PDFs, images, code all work).
- Voice/video is pre-transcribed; prefer `transcript=` over re-transcribing.
- `[Document: name]` with NO `<attachment path=…>` tag means the file did
  NOT arrive. Never claim you read it. Reply: "The file didn't reach me —
  please re-share as a file attachment." Log it with `/issues`.
