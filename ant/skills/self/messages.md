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
