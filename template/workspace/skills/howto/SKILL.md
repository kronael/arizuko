---
name: howto
description: Generate a getting-started howto page for this kanipi instance. Deploys to /web/howto/. Use when asked to create onboarding, setup guide, or howto page.
---

# Howto

Generate a getting-started page at `/web/howto/index.html`
that guides new users through connecting to this instance.

## When to use

- First-time instance setup
- User asks for a howto, guide, or onboarding page
- Instance has no howto yet

## Design requirements

Single self-contained HTML file. No build step.

- Tailwind CSS via CDN (`https://cdn.tailwindcss.com`)
- Dark/light theme toggle (localStorage persistence)
- Mobile-first responsive design
- Clean card-based layout with numbered steps
- Code blocks with terminal styling (dark bg, colored syntax)
- Subtle dot-pattern background

### Color palette (configure in tailwind.config)

Use warm earth tones for a professional, calm feel:

```js
leather: {
  50 - 900;
} // base text and borders
hay: {
  400 - 600;
} // accents, step numbers
field: {
  400 - 600;
} // success, green accents
copper: {
  400 - 600;
} // code highlights
sky: {
  400 - 600;
} // info, links
```

### Component patterns

- **Step cards**: numbered steps with colored top strip,
  icon number badge, content area
- **Code blocks**: dark rounded container with dot header
  (red/yellow/green dots like terminal), monospace content
- **Info banner**: dismissible (localStorage), subtle bg,
  explains this is a template page

## Content structure

### Dismissible banner (top)

"This is the getting-started guide. Once you're set up,
your agent can deploy apps here." Dismiss button stores
`howto-dismissed` in localStorage. Check on load, hide
if dismissed.

### Hero

Instance name as title. Subtitle: what this bot does
(Claude AI agent on telegram/discord/whatsapp).

### Steps (numbered cards)

1. **Prerequisites** — Docker on server, messaging account
   (Telegram and/or Discord), Claude OAuth token

2. **Talk to the bot** — How to start a conversation.
   On Telegram: find the bot by username, send a message.
   On Discord: add bot to server, mention it in a channel.
   The bot responds to @mentions or direct messages.

3. **What it can do** — Read/write files, run commands,
   search the web, build web apps, schedule tasks.
   Each conversation gets its own isolated container.

4. **Web apps** — The bot can create web apps for you.
   Ask it to build something and it deploys to this site.
   Example prompts: "build me a todo app", "create a
   dashboard for X".

5. **Tips** — Be specific in requests. The bot remembers
   conversation context. Use @mention in group chats.
   Long tasks run in background containers.

### Features grid (bottom)

3-4 feature cards: multi-channel, container isolation,
web deployment, scheduled tasks.

## Customization

Read instance context before generating:

1. Check `$ASSISTANT_NAME` or hostname for the bot name
2. Check which channels are configured (presence of
   TELEGRAM_BOT_TOKEN, DISCORD_BOT_TOKEN in env)
3. Check if web apps already exist (`ls /web/`)
4. Tailor the content to what's actually available

## After deploying

1. Update `/web/index.html` hub to include howto link
2. Verify page loads: `curl -s http://localhost:$VITE_PORT/howto/`
3. Tell the user the URL

## Language

Write in the same language the user is communicating in.
Default to English if unclear.

## Reference implementation

Use `/srv/app/template/web/howto/index.html` as the base.
Copy it to `/web/howto/index.html` and customize:

- Replace "kanipi" with `$ASSISTANT_NAME` in title/hero
- Update subtitle to match what this specific instance does
- Remove steps that don't apply (e.g. clone/build for
  users who just chat with the bot, not deploy it)
- Add instance-specific info (channel links, web host URL)
- Translate to user's language if not English

The template has the full design system already wired:
tailwind config, color palette, depth/glow classes,
code blocks, step cards, theme toggle, dismissible banner.
Do NOT rebuild from scratch — copy and adapt.
