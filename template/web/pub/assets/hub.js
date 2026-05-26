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

  function injectFooter() {
    // Site-wide footer with the two anchors the operator wants on every page:
    // GitHub source + the canonical krons-hosted instance.
    if (document.querySelector('.hub-footer')) return;
    var f = document.createElement('footer');
    f.className = 'hub-footer';
    f.innerHTML =
      '<a href="https://github.com/kronael/arizuko">github.com/kronael/arizuko</a>' +
      ' · <a href="https://krons.fiu.wtf/pub/arizuko/">krons.fiu.wtf</a>';
    document.body.appendChild(f);
  }

  // AI handoff — routes the visitor to the krons arizuko agent, prefilled
  // with the current page URL as context. Token is public by design; chat
  // surface is rate-limited at the webd layer (chat_mcp.go).
  function injectAskAgent() {
    if (document.querySelector('.ask-agent')) return;
    var token = 'G6CffSXGc5gBqNUtcwE-cm3hNT1P7TOiSNNPru1MP3Y';
    var ref = encodeURIComponent(window.location.pathname);
    var a = document.createElement('a');
    a.className = 'ask-agent';
    a.href = 'https://krons.fiu.wtf/chat/' + token + '/?ref=' + ref;
    a.target = '_blank';
    a.rel = 'noopener';
    a.title =
      'Open chat with the arizuko agent — it has the codebase + docs in context.';
    a.textContent = 'Ask the agent →';
    document.body.appendChild(a);
  }

  function init() {
    initTheme();
    var btn = document.querySelector('.theme-toggle');
    if (btn) btn.addEventListener('click', toggle);
    var grid = document.querySelector('.grid');
    var empty = document.querySelector('.empty');
    if (grid && empty && grid.children.length > 0) empty.style.display = 'none';
    injectFooter();
    injectAskAgent();
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else {
    init();
  }
})();
