## Release announcements work again

Two things stopped the announce step for ten weeks (BUGS `L89`).

`/opt/arizuko/CHANGELOG.md` was not in the image, so step (e) had nothing to
read and reported "no CHANGELOG, nothing to announce" as a clean no-op. The
image now carries it.

The destination was built from the route `match` by taking `room=` as if it
were a JID. It is not: `chat_jid` is the whole JID and `platform`+`room` are
its two halves. Step (e) now builds `<platform>:<room>`, uses `chat_jid=`
verbatim when present, and FAILS instead of announcing nowhere when a tagged
route names no platform.

Announcing stays opt-in. A group announces only to routes whose target carries
`#announce`, and that route's match must carry `chat_jid=` or `platform=`.
