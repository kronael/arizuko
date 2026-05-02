# Cookbooks page — content spec

{{TAGLINE}}

Page purpose: scenario-shaped recipes that combine multiple primitives into
a runnable workflow. Each recipe is small enough to execute today, generic
enough to apply to any deployment.

Tone: direct. Lead with the scenario, list the steps, end with one watch-out.
No marketing fluff, no "users can…" voice — second-person where natural.

Structure: TLDR grid at top (one card per recipe, 2–3 words), then full
recipes below. Each recipe: title, one-line scenario, numbered steps with
concrete tool calls, one watch-out note.

---

## 01 — Morning Daily Digest as Voice

**Scenario:** A 9 AM digest that researches overnight news, summarises it, and speaks the result back on platforms that support voice.

1. Ask the agent to schedule the digest: `cron 0 9 * * *`, prompt "research overnight news for <topics>, summarise to 6 bullets."
2. Inside the prompt, branch on the target JID — call `send_voice` on Telegram and WhatsApp; fall back to `send` everywhere else (see howto 04b, 17).
3. Keep the prompt self-contained — no "continue from yesterday." Scheduled tasks run isolated, no memory injection (howto 10).
4. Verify the next run on `/dash/tasks/`, or ask "is the morning digest scheduled?"

**Watch out:** Voice synthesis caps at 5000 chars. If the summary grows beyond a tight bullet list, the call will truncate or error — keep the prompt explicit about brevity.

---

## 02 — Respond to a DM by Linking Back to a Group

**Scenario:** A user DMs the agent. You want the group chat to see the DM context and join the reply.

1. On inbound DM, call `get_thread` to load the topic-scoped conversation (howto 08).
2. Compose a short summary — sender, platform, gist of the request.
3. `send_message` to the linked group with the summary plus the slink URL for the DM topic so group members can join.
4. Reply in the DM normally; the group thread receives a parallel notification.

**Watch out:** Don't post the slink token publicly outside the linked group — anyone with the URL gets unauthenticated access. Keep the slink-bearing message inside the working group.

---

## 03 — Multi-Agent Handoff via Slink-MCP

**Scenario:** Agent A drives Agent B's group as a remote tool, fully MCP-mediated, no HTTP polling.

1. In Agent B's group, mint a slink token (or reuse an existing one).
2. Register `https://<host>/slink/<token>/mcp` as a remote MCP server in Agent A's runtime (howto 16a).
3. Agent A calls `send_message` to inject a prompt into Agent B.
4. Agent A polls `get_round` for the reply, or calls `steer` mid-run if the task drifts.
5. Wrap the loop in a script — the transport is stateless and idempotent.

**Watch out:** No message history flows over the MCP transport. If Agent A needs prior context, fetch it via Agent B's normal MCP tools (`inspect_messages`, `get_thread`) before issuing the handoff.

---

## 04 — Adaptive File Delivery by Adapter

**Scenario:** A scheduled task that delivers the same content as voice on Telegram, audio attachment on Discord, and a hosted link on Bluesky.

1. Schedule the task with the inbound JID list as input.
2. Inside the prompt, call `inspect_routing` to confirm the active JID type.
3. Branch on the typed JID prefix (howto 08b): `telegram:*` → `send_voice`; `discord:*` → `send_file` with audio; `bluesky:*` → host the file under `/workspace/web/pub/` and `send` the link.
4. Each branch uses the same source content; only the delivery primitive changes.

**Watch out:** Bluesky and other no-file adapters silently drop `send_file` calls. Always check the adapter capability matrix (howto 04c) before assuming a delivery succeeded — log the platform's response, not just "sent."

---

## 05 — Account Linking Onboarding

**Scenario:** A new user logs in via GitHub but uses Discord daily. Get them linked before any collision happens.

1. After first login, the agent surfaces a one-line nudge: "want to link your Discord too?" (howto 09a).
2. User opens `/dash/profile` and clicks "Link account → Discord".
3. OAuth round-trip completes; both providers now resolve to the same canonical sub.
4. Next login from either provider lands in the same workspace, same memory, same files.

**Watch out:** If the user has already chatted with the agent under a Discord identity *without* logging in, the link will trigger the collision UX. Walk them through the choice — link or become — instead of leaving them on the dialog.

---

## 06 — Thread-Scoped Recall for Support

**Scenario:** A support agent replies to a DM ticket using only that thread's context, never mixing in unrelated chats.

1. On inbound message, call `get_thread` (not `fetch_history` — the thread is what matters here, see howto 08).
2. Pass the thread to `/recall-memories` to surface user facts from `users/<id>.md`.
3. Compose a reply grounded in the thread + user facts; nothing else.
4. Optionally `send_file` for canned solutions or screenshots.

**Watch out:** `get_thread` returns the topic-scoped slice. If the user opened a new `#topic` mid-ticket, the previous thread won't be in scope — confirm with `inspect_session` before assuming context is complete.

---

## 07 — Offline-Safe Scheduled Cron with Isolated Context

**Scenario:** A weather forecast or data sync that runs alone, can't leak user data, and doesn't depend on chat history.

1. Pick a quiet group (or sub-group) where no real users chat. The task runs in container isolation; the host group only receives the output.
2. Schedule `cron` or `interval` with a self-contained prompt — every input the task needs is inlined.
3. Avoid references to `diary/`, `users/`, or "yesterday" — scheduled tasks have no memory injection (howto 10).
4. Output goes to the host group via `send` or `send_file`. Optional: pipe to a slink so external systems can subscribe.

**Watch out:** Scheduled containers exit after the run. State that needs to persist across runs goes into a file under `/workspace/` written by the task, not into agent memory — memory layers aren't loaded for scheduled work.
