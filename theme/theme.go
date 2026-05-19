package theme

import (
	"html"
	"html/template"
)

// CSS is the shared arizuko stylesheet. All theme variables, resets,
// and component classes live here. Consumers embed it via Head() or
// Page(), or inline it directly inside <style> tags.
const CSS = `
:root {
  --bg: #0a0a0a; --fg: #e0e0e0;
  --accent: #58a6ff; --dim: #888; --border: #222;
  --card: #111; --card-hover: #161616;
  --code-bg: #1a1a1a;
  --danger: #e5484d; --warn: #fa0; --ok: #4ade80;
  --shadow: 0 1px 3px rgba(0,0,0,.4), 0 1px 2px rgba(0,0,0,.3);
  --shadow-lg: 0 4px 12px rgba(0,0,0,.5);
  --radius: 8px;
  --transition: .15s ease;
}
[data-theme=light] {
  --bg: #fafafa; --fg: #1a1a1a;
  --accent: #0969da; --dim: #6e7681; --border: #ddd;
  --card: #fff; --card-hover: #f5f5f5;
  --code-bg: #f0f0f0;
  --danger: #cf222e; --warn: #b85d00; --ok: #1a7f37;
  --shadow: 0 1px 3px rgba(0,0,0,.08), 0 1px 2px rgba(0,0,0,.06);
  --shadow-lg: 0 4px 12px rgba(0,0,0,.1);
}

*, *::before, *::after { box-sizing: border-box; margin: 0; padding: 0; }

body {
  font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto,
               "Helvetica Neue", Arial, sans-serif;
  font-size: 14px;
  line-height: 1.55;
  color: var(--fg);
  background: var(--bg);
  -webkit-font-smoothing: antialiased;
}
code, pre, .mono, .id {
  font-family: "SF Mono", "JetBrains Mono", Consolas, "Liberation Mono", monospace;
  font-size: .92em;
}
code, .id {
  background: var(--code-bg);
  padding: 1px 5px;
  border-radius: 3px;
  border: 1px solid var(--border);
}
pre code { background: none; padding: 0; border: 0; }

/* --- Layout --- */
.page-center {
  display: flex; justify-content: center; align-items: center;
  min-height: 100vh; padding: 1rem;
}
.page-wide {
  max-width: 1100px; margin: 0 auto;
  padding: 2rem 1.5rem;
}

/* --- Cards --- */
.card {
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  box-shadow: var(--shadow);
  padding: 1.5rem;
}
.card-sm { max-width: 400px; width: 100%; }
.card-md { max-width: 520px; width: 100%; }
.card-lg { max-width: 680px; width: 100%; }
.card-full { width: 100%; }

/* --- Typography --- */
.brand {
  color: var(--fg);
  font-weight: 600;
  font-size: 1.2em;
  letter-spacing: -.01em;
}
h1 { font-size: 1.5em; color: var(--fg); font-weight: 600; margin-bottom: .25em; }
h2 {
  font-size: 1.05em; color: var(--fg); font-weight: 600;
  margin: 1.4em 0 .6em;
  padding-bottom: .3em;
  border-bottom: 1px solid var(--border);
}
h3 { font-size: .95em; color: var(--fg); font-weight: 600; margin: 1em 0 .4em; }
.crumbs { color: var(--dim); font-size: .85em; margin-bottom: .25em; }
.crumbs a { color: var(--dim); }
.crumbs a:hover { color: var(--accent); }
p { margin: .4em 0; }
.dim { color: var(--dim); font-size: .85em; }
.sub { color: var(--dim); font-size: .85em; text-align: center; margin: 0 0 1.2em; }

/* --- Links --- */
a { color: var(--accent); text-decoration: none; transition: color var(--transition); }
a:hover { text-decoration: underline; }

/* --- Forms --- */
input, select {
  width: 100%;
  padding: .55rem .75rem;
  margin: .25rem 0;
  border: 1px solid var(--border);
  border-radius: 6px;
  background: var(--bg);
  color: var(--fg);
  font-family: inherit;
  font-size: .9em;
  transition: border-color var(--transition), box-shadow var(--transition);
}
input:focus, select:focus {
  outline: none;
  border-color: var(--accent);
  box-shadow: 0 0 0 2px rgba(88,166,255,.15);
}
input::placeholder { color: var(--dim); }

/* --- Buttons --- */
button, .btn {
  display: inline-block;
  padding: .55rem 1.2rem;
  background: var(--accent);
  color: #fff;
  border: none;
  border-radius: 6px;
  cursor: pointer;
  font-family: inherit;
  font-weight: 600;
  font-size: .9em;
  transition: opacity var(--transition);
  text-decoration: none;
  text-align: center;
}
button:hover, .btn:hover { opacity: .88; }
.btn-danger { background: var(--danger); color: #fff; }
.btn-secondary {
  background: transparent;
  color: var(--fg);
  border: 1px solid var(--border);
}
.btn-secondary:hover { border-color: var(--accent); color: var(--accent); }

/* --- OAuth buttons --- */
.sep { color: var(--dim); text-align: center; margin: 1em 0 .5em; font-size: .8em; }
.oauth-btn {
  display: block; width: 100%;
  padding: .55rem; margin-top: .4em;
  background: var(--bg); color: var(--fg);
  border: 1px solid var(--border); border-radius: 6px;
  text-align: center; text-decoration: none; font-size: .9em;
  transition: border-color var(--transition), color var(--transition);
}
.oauth-btn:hover { border-color: var(--accent); color: var(--accent); text-decoration: none; }

/* --- Tables --- */
table { border-collapse: collapse; width: 100%; font-size: .9em; margin: .5rem 0; }
th, td { text-align: left; padding: .5rem .75rem; border-bottom: 1px solid var(--border); }
th {
  color: var(--dim); font-weight: 600;
  font-size: .8em;
  background: var(--bg);
  position: sticky; top: 0;
  border-bottom: 1px solid var(--border);
}
tbody tr:nth-child(even) td { background: rgba(127,127,127,.04); }
tbody tr:hover td { background: var(--card-hover); }
td:first-child { white-space: nowrap; }
.num { text-align: right; font-variant-numeric: tabular-nums; font-family: "SF Mono", monospace; }
.empty { color: var(--dim); font-style: italic; padding: 1.5rem; text-align: center; }
.banner-err { color: var(--danger); padding: .75rem; border: 1px solid var(--danger); border-radius: 6px; background: rgba(229,72,77,.05); margin: .5rem 0; }

/* --- Status dots --- */
.dot {
  display: inline-block; width: 8px; height: 8px;
  border-radius: 50%; margin-left: .4em; vertical-align: middle;
}
.dot-ok { background: var(--ok); }
.dot-warn { background: var(--warn); }
.dot-err { background: var(--danger); }
.ok { background: var(--ok); }
.warn { background: var(--warn); }
.err { background: var(--danger); }

/* --- Grid tiles (dashd portal) --- */
.tiles {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
  gap: .8rem; margin-top: 1em;
}
.tile {
  background: var(--card); border: 1px solid var(--border);
  border-radius: var(--radius); padding: 1em; color: var(--fg);
  transition: border-color var(--transition), box-shadow var(--transition);
}
.tile:hover { border-color: var(--accent); box-shadow: var(--shadow); text-decoration: none; }
.tile h2 { margin: 0 0 .3em; font-size: .95em; color: var(--fg); border: none; padding: 0; font-weight: 600; }
.tile p { color: var(--dim); font-size: .85em; margin: 0; }

/* --- Nav (active state for breadcrumbs) --- */
nav a[aria-current="page"] {
  color: var(--accent);
  border-bottom: 2px solid var(--accent);
  padding-bottom: 2px;
}

/* --- Code --- */
pre, code { background: var(--code-bg); border-radius: 3px; }
code { padding: .15em .4em; font-size: .9em; }
pre { padding: 1em; overflow: auto; max-height: 400px; border: 1px solid var(--border); }

/* --- Details/Accordion --- */
details { margin: .3em 0; }
details summary { cursor: pointer; padding: .3em 0; color: var(--fg); }
details summary:hover { color: var(--accent); }
.group-detail {
  margin: .5em 0 .5em 1em; font-size: .9em;
  padding-left: .8em; border-left: 1px solid var(--border);
}

/* --- Banners --- */
.banner-ok, .banner-warn, .banner-err {
  padding: .6em 1em; margin: 1em 0; border-radius: 6px; border: 1px solid;
}
.banner-ok { background: rgba(74,222,128,.08); border-color: var(--accent); color: var(--accent); }
.banner-warn { background: rgba(255,170,0,.08); border-color: var(--warn); color: var(--warn); }
.banner-err { background: rgba(229,72,77,.08); border-color: var(--danger); color: var(--danger); }

/* --- Nav (dashd) --- */
nav { margin: .4em 0 1.4em; color: var(--dim); font-size: .9em; }
nav a { color: var(--dim); margin-right: 1rem; }
nav a:hover { color: var(--accent); }

/* --- Theme toggle --- */
.theme-toggle {
  position: fixed; top: 1em; right: 1em;
  background: var(--card); border: 1px solid var(--border);
  border-radius: 50%; width: 2.2em; height: 2.2em;
  cursor: pointer; font-size: 1em; color: var(--fg);
  transition: transform .3s;
}
.theme-toggle:hover { transform: rotate(20deg); }

/* --- Two-column responsive layout --- */
.cols {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 1.2rem;
  margin: 1rem 0;
}
.cols > .card-full { grid-column: 1 / -1; }

/* --- Section cards (onbod dashboard) --- */
.section {
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  padding: 1.2rem 1.4rem;
  box-shadow: var(--shadow);
}
.section h3 {
  margin: 0 0 .6em;
  padding-bottom: .3em;
  border-bottom: 1px solid var(--border);
}
.section table { margin: 0; }

/* --- User header --- */
.user-header {
  display: flex; align-items: center; gap: .8rem;
  margin-bottom: 1.2rem;
}
.user-avatar {
  width: 2.8rem; height: 2.8rem;
  border-radius: 50%;
  background: var(--accent);
  color: var(--bg);
  display: flex; align-items: center; justify-content: center;
  font-size: 1.2em; font-weight: bold;
  flex-shrink: 0;
}
.user-meta .dim { margin-top: .1em; }

/* --- Empty states --- */
.empty {
  color: var(--dim); font-size: .85em;
  padding: .6rem .7rem;
  font-style: italic;
}

/* --- Responsive --- */
@media (max-width: 768px) {
  body { font-size: 13px; }
  .page-wide { padding: 1.5rem 1rem; }
  .cols { grid-template-columns: 1fr; }
  .card { padding: 1.2rem; }
}
@media (max-width: 480px) {
  body { font-size: 12px; }
  .page-wide { padding: 1rem .8rem; }
  .card { padding: 1rem; }
}

/* --- htmx indicators --- */
.htmx-indicator { display: none; }
.htmx-request .htmx-indicator { display: inline; }
#global-spinner.htmx-indicator { display: none; }
.htmx-request#global-spinner { display: block; }

/* --- Form rows --- */
.form-row { margin: 0.5em 0; }
.form-row label { display: flex; align-items: baseline; gap: 0.5em; }
`

// ThemeScript initializes the theme from localStorage or system preference.
// Must run before body renders to prevent flash of wrong theme.
const ThemeScript = `<script>(function(){var t=localStorage.getItem('hub-theme')||(matchMedia('(prefers-color-scheme: dark)').matches?'dark':'light');document.documentElement.setAttribute('data-theme',t)})();</script>`

// ToggleScript adds the theme toggle button behavior. Include after body
// content or at end of head. Requires a <button class="theme-toggle"> in
// the DOM.
const ToggleScript = `<script>(function(){window.toggleTheme=function(){var c=document.documentElement.getAttribute('data-theme')==='dark'?'light':'dark';document.documentElement.setAttribute('data-theme',c);localStorage.setItem('hub-theme',c);var b=document.querySelector('.theme-toggle');if(b)b.textContent=c==='dark'?'\u{1F319}':'\u{1F506}'};document.addEventListener('DOMContentLoaded',function(){var b=document.querySelector('.theme-toggle');if(b){b.textContent=document.documentElement.getAttribute('data-theme')==='dark'?'\u{1F319}':'\u{1F506}';b.addEventListener('click',toggleTheme)}})})();</script>`

func Head(title string) string {
	return `<head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>arizuko — ` +
		html.EscapeString(title) + `</title><style>` + CSS + `</style>` + ThemeScript + `</head>`
}

// body is template.HTML: callers MUST escape any user input
// (html.EscapeString / template.HTMLEscapeString) before wrapping with
// template.HTML(...).
func Page(title string, body template.HTML) string {
	return `<!DOCTYPE html><html>` + Head(title) +
		`<body><div class="page-center"><div class="card card-md"><p class="brand">arizuko</p><h2>` +
		html.EscapeString(title) + `</h2>` + string(body) +
		`</div></div></body></html>`
}
