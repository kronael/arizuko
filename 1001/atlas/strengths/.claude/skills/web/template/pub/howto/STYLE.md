# Howto page — style generation guide

When building the howto page, generate a coherent visual style from scratch.
Do NOT copy the old leather/earth-tone template. Pick from the axes below and
combine them into a self-consistent design system. The goal is variety across
deployments — each instance should look distinctly different.

---

## Step 1 — Pick a palette archetype

Choose ONE. Derive all colors (bg, text, accent, code block, border) from it.

| Archetype         | Feel               | Example accents                               |
| ----------------- | ------------------ | --------------------------------------------- |
| **slate-ink**     | Corporate, calm    | #334155 bg · #0ea5e9 accent · #f1f5f9 surface |
| **forest-mist**   | Natural, cool      | #1a2e1a bg · #4ade80 accent · #f0fdf4 surface |
| **amber-desk**    | Warm, editorial    | #2e1f13 bg · #d97706 accent · #fffbeb surface |
| **violet-lab**    | Technical, sharp   | #1e1b4b bg · #a78bfa accent · #f5f3ff surface |
| **rose-paper**    | Soft, approachable | #fff1f2 bg · #e11d48 accent · #fdf2f8 surface |
| **zinc-terminal** | Minimal, stark     | #18181b bg · #22d3ee accent · #fafafa surface |
| **ocean-deep**    | Rich, confident    | #0c1a2e bg · #38bdf8 accent · #e0f2fe surface |
| **sage-clay**     | Organic, muted     | #f4f1ec bg · #6b7c5e accent · #fafaf8 surface |

Or: pick a URL, screenshot it, extract its dominant palette.

---

## Step 2 — Pick a typography pair

| Pair          | Heading              | Body              | Code           |
| ------------- | -------------------- | ----------------- | -------------- |
| **geometric** | Space Grotesk 700    | Inter 400         | JetBrains Mono |
| **editorial** | Playfair Display 700 | Source Serif 400  | Fira Code      |
| **technical** | IBM Plex Sans 600    | IBM Plex Sans 400 | IBM Plex Mono  |
| **humanist**  | DM Sans 700          | DM Sans 400       | DM Mono        |
| **slab**      | Roboto Slab 700      | Roboto 400        | Roboto Mono    |
| **system**    | (system-ui, bold)    | system-ui         | monospace      |

---

## Step 3 — Pick a layout density

| Density      | Card style                                                | Spacing               |
| ------------ | --------------------------------------------------------- | --------------------- |
| **compact**  | tight cards, 1-col prose                                  | py-6 px-5, small gaps |
| **airy**     | large cards, generous margins                             | py-12 px-8, wide gaps |
| **magazine** | 2-col grid on desktop, full bleed headers                 | mixed                 |
| **terminal** | no cards — plain pre-formatted text, monospace everything | dense                 |

---

## Step 4 — Pick a decoration level

| Level      | What it means                                                 |
| ---------- | ------------------------------------------------------------- |
| **bare**   | No shadows, no gradients, flat borders only                   |
| **subtle** | Light shadows, 1px borders, no background textures            |
| **rich**   | Colored card top-strips, dot-grid background, glow-on-hover   |
| **vivid**  | Gradient headers, colored section icons, animated hover lifts |

---

## Step 5 — Dark mode strategy

Always implement dark mode. Pick one:

- **invert** — swap bg/surface, keep accent unchanged
- **desaturate** — shift accent 20% toward grey in dark mode
- **deepen** — darken bg further, lighten text, accent stays saturated

Toggle: fixed button top-right, `data-theme="dark"` on `<html>`.

---

## Combining the choices

Example A — **zinc-terminal + technical + compact + bare + invert**:
Monospaced everything, stark contrast, no card decoration.

Example B — **violet-lab + geometric + airy + vivid + deepen**:
Large cards with gradient strips, purple glow on hover, spacious layout.

Example C — **sage-clay + editorial + magazine + subtle + desaturate**:
Serif headings, organic palette, 2-col grid, tasteful shadows.

---

## Rules

- Derive every color from the chosen archetype — no ad hoc hex values
- Code blocks: dark background regardless of page theme (exception: terminal layout)
- TLDR grid: visually distinct from section cards (smaller, tighter)
- Mobile: single column always
- Footer MUST read: `powered by <a href="https://REDACTED/arizuko">arizuko</a>`
- NEVER attribute to Anthropic or Claude
- Use CDN fonts (Google Fonts or Bunny Fonts) — no local font files
- Tailwind CDN is fine; inline `<style>` for custom tokens
