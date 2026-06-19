(function () {
  function initTheme() {
    var saved = localStorage.getItem('hub-theme');
    var theme =
      saved ||
      (matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light');
    document.documentElement.setAttribute('data-theme', theme);
    updateBtn(theme);
  }

  function toggle() {
    var cur = document.documentElement.getAttribute('data-theme') || 'dark';
    var next = cur === 'dark' ? 'light' : 'dark';
    document.documentElement.setAttribute('data-theme', next);
    localStorage.setItem('hub-theme', next);
    updateBtn(next);
  }

  function updateBtn(theme) {
    var btn = document.querySelector('.theme-toggle');
    if (btn) btn.textContent = theme === 'dark' ? '\u{1F506}' : '\u{1F319}';
  }

  // ARIZUKO_VERSION is the docs-rendered version stamp. Updated per
  // release via the sync workflow (rsync template/web/pub/ → krons
  // re-injects this string from CHANGELOG.md latest [vX.Y.Z] header).
  // Until automated: bumped manually with each release.
  var ARIZUKO_VERSION = 'v0.55.0';

  function injectFooter() {
    // Site-wide footer with the version stamp + the two anchors the
    // operator wants on every page: GitHub source + canonical instance.
    if (document.querySelector('.hub-footer')) return;
    var f = document.createElement('footer');
    f.className = 'hub-footer';
    f.innerHTML =
      '<span class="hub-version">arizuko ' +
      ARIZUKO_VERSION +
      '</span> · ' +
      '<a href="https://github.com/kronael/arizuko">github.com/kronael/arizuko</a>' +
      ' · <a href="https://krons.fiu.wtf/pub/arizuko/">krons.fiu.wtf</a>' +
      ' · <a href="https://krons.fiu.wtf/pub/arizuko/legacy/">previous docs</a>';
    document.body.appendChild(f);
  }

  // AI handoff — routes the visitor to the krons arizuko agent, prefilled
  // with the current page URL as context. Token is public by design; chat
  // surface is rate-limited at the webd layer (chat_mcp.go).
  var AGENT_TOKEN = 'G6CffSXGc5gBqNUtcwE-cm3hNT1P7TOiSNNPru1MP3Y';
  function chatURL(extra) {
    var ref = encodeURIComponent(window.location.pathname);
    var u = 'https://krons.fiu.wtf/chat/' + AGENT_TOKEN + '/?ref=' + ref;
    if (extra) u += '&' + extra;
    return u;
  }

  function injectAskAgent() {
    if (document.querySelector('.ask-agent')) return;
    var a = document.createElement('a');
    a.className = 'ask-agent';
    a.href = chatURL();
    a.target = '_blank';
    a.rel = 'noopener';
    a.title =
      'Open chat with the arizuko agent — it has the codebase + docs in context.';
    a.textContent = 'Ask the agent →';
    document.body.appendChild(a);
  }

  // Select-to-ask — popup near any text selection of reasonable length.
  // Click → opens chat with selection + page URL as context. Replaces
  // search (the agent IS the search). Pure JS, no service calls.
  function injectSelectionPopup() {
    var popup;
    function ensure() {
      if (popup) return popup;
      popup = document.createElement('a');
      popup.className = 'ask-selection';
      popup.target = '_blank';
      popup.rel = 'noopener';
      popup.textContent = 'Ask about this →';
      popup.style.display = 'none';
      document.body.appendChild(popup);
      return popup;
    }
    function hide() {
      if (popup) popup.style.display = 'none';
    }
    document.addEventListener('mouseup', function () {
      // Defer one frame so the selection settles after click-to-deselect.
      setTimeout(function () {
        var sel = window.getSelection();
        var text = sel ? sel.toString().trim() : '';
        if (!text || text.length < 3 || text.length > 500) {
          hide();
          return;
        }
        var r;
        try {
          r = sel.getRangeAt(0).getBoundingClientRect();
        } catch (e) {
          return;
        }
        if (!r || (r.width === 0 && r.height === 0)) return;
        var p = ensure();
        p.href = chatURL('sel=' + encodeURIComponent(text));
        p.style.top = window.scrollY + r.top - 34 + 'px';
        p.style.left = window.scrollX + r.left + 'px';
        p.style.display = 'inline-block';
      }, 0);
    });
    document.addEventListener('mousedown', function (e) {
      if (popup && e.target !== popup) hide();
    });
    document.addEventListener('keydown', function (e) {
      if (e.key === 'Escape') hide();
    });
    window.addEventListener('scroll', hide, { passive: true });
  }

  // ── docs three-pane chrome ──────────────────────────────────────────
  // Only fires on pages that opt in with .docs-layout. The reference and
  // concepts shells inline their own left-nav tree (renders before this
  // script); buildTOC fills the right rail; markCurrentNav highlights the
  // active link; the drawer is the <1200px collapse of the left nav.

  function slug(s) {
    return s
      .toLowerCase()
      .replace(/[^a-z0-9]+/g, '-')
      .replace(/^-+|-+$/g, '');
  }

  // Walk .docs-content h2,h3 into the "On this page" rail. Assign slug ids
  // to headings that lack one, de-duped with -2/-3. Render nothing when a
  // page has fewer than two headings.
  function buildTOC() {
    var toc = document.querySelector('.docs-toc');
    var content = document.querySelector('.docs-content');
    if (!toc || !content) return;
    var heads = content.querySelectorAll('h2, h3');
    if (heads.length < 2) {
      toc.remove();
      return;
    }
    var seen = {};
    var ul = document.createElement('ul');
    heads.forEach(function (h) {
      if (!h.id) {
        var base = slug(h.textContent) || 'section';
        var id = base;
        var n = 2;
        while (seen[id] || document.getElementById(id)) {
          id = base + '-' + n++;
        }
        h.id = id;
      }
      seen[h.id] = true;
      var li = document.createElement('li');
      if (h.tagName === 'H3') li.className = 'toc-h3';
      var a = document.createElement('a');
      a.href = '#' + h.id;
      a.textContent = h.textContent;
      li.appendChild(a);
      ul.appendChild(li);
    });
    var label = document.createElement('div');
    label.className = 'toc-label';
    label.textContent = 'On this page';
    toc.appendChild(label);
    toc.appendChild(ul);
  }

  // Normalize a pathname so `…/x/` and `…/x/index.html` compare equal and a
  // trailing slash never trips the match.
  function normPath(p) {
    p = p.replace(/index\.html$/, '');
    if (p.length > 1) p = p.replace(/\/$/, '');
    return p;
  }

  // Set aria-current="page" on the nav link pointing at this page, matching
  // on the resolved (absolute) href so relative `../x.html` links normalize
  // against the browser's own resolution.
  function markCurrentNav() {
    var nav = document.querySelector('.docs-nav');
    if (!nav) return;
    var here = normPath(window.location.pathname);
    var links = nav.querySelectorAll('a[href]');
    links.forEach(function (a) {
      if (normPath(new URL(a.href, window.location.href).pathname) === here) {
        a.setAttribute('aria-current', 'page');
      }
    });
  }

  // <1200px: the left nav collapses behind a topbar button. aria-expanded
  // tracks state; Esc and an outside click close it. No persistence.
  function initNavDrawer() {
    var toggle = document.querySelector('.docs-navtoggle');
    var nav = document.querySelector('.docs-nav');
    if (!toggle || !nav) return;
    function open() {
      nav.removeAttribute('hidden');
      toggle.setAttribute('aria-expanded', 'true');
    }
    function close() {
      nav.setAttribute('hidden', '');
      toggle.setAttribute('aria-expanded', 'false');
    }
    // Drawer is closed by default below 1200px; above, CSS forces it visible
    // regardless of the hidden attribute, so a stale hidden attr is harmless.
    close();
    toggle.addEventListener('click', function (e) {
      e.stopPropagation();
      if (toggle.getAttribute('aria-expanded') === 'true') close();
      else open();
    });
    document.addEventListener('keydown', function (e) {
      if (e.key === 'Escape') close();
    });
    document.addEventListener('click', function (e) {
      if (
        toggle.getAttribute('aria-expanded') === 'true' &&
        !nav.contains(e.target) &&
        e.target !== toggle
      ) {
        close();
      }
    });
  }

  function init() {
    initTheme();
    var btn = document.querySelector('.theme-toggle');
    if (btn) btn.addEventListener('click', toggle);
    var grid = document.querySelector('.grid');
    var empty = document.querySelector('.empty');
    if (grid && empty && grid.children.length > 0) empty.style.display = 'none';
    buildTOC();
    markCurrentNav();
    initNavDrawer();
    injectFooter();
    injectAskAgent();
    injectSelectionPopup();
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else {
    init();
  }
})();
