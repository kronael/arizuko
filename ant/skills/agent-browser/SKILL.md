---
name: agent-browser
description: CDP-based Chromium browser automation.
when_to_use: >
  Use for JS-rendered pages, auth flows, form submission, screenshots, or data
  extraction. Prefer over curl for interactive or dynamic pages.
allowed-tools: Bash(agent-browser:*)
---

# agent-browser

## Core workflow

1. `agent-browser open <url>`
2. `agent-browser snapshot -i` — interactive elements get refs like `@e1`
3. Interact using refs
4. Re-snapshot after navigation or DOM changes

## Commands

```bash
# Navigation
agent-browser open <url> | back | forward | reload | close

# Snapshot
agent-browser snapshot [-i interactive] [-c compact] [-d depth] [-s selector]

# Interact
agent-browser click|dblclick|hover @e1
agent-browser fill @e2 "text"          # clear + type
agent-browser type @e2 "text"          # type, no clear
agent-browser press Enter
agent-browser check|uncheck @e1
agent-browser select @e1 "value"
agent-browser scroll down 500
agent-browser upload @e1 file.pdf

# Read
agent-browser get text|html|value @e1
agent-browser get attr @e1 href
agent-browser get title|url
agent-browser get count ".item"

# Screenshot / PDF
agent-browser screenshot [path.png] [--full]
agent-browser pdf output.pdf

# Wait
agent-browser wait @e1 | 2000 | --text "Success" | --url "pattern" | --load networkidle

# Semantic locators
agent-browser find role button click --name "Submit"
agent-browser find text "Sign In" click
agent-browser find label "Email" fill "user@test.com"

# State / storage
agent-browser state save|load auth.json
agent-browser cookies [set name value | clear]
agent-browser storage local [set k v]
agent-browser eval "document.title"
```

## Coordinate clicks

ALWAYS use `mouse` for coordinate-based clicks — JS `MouseEvent` has
`isTrusted=false` and is blocked by security-conscious apps. `mouse` sends
real CDP input events with `isTrusted=true`.

```bash
agent-browser mouse move 930 120
agent-browser mouse down
agent-browser mouse up
```
