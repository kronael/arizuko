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

  function init() {
    initTheme();
    var btn = document.querySelector('.theme-toggle');
    if (btn) btn.addEventListener('click', toggle);
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else {
    init();
  }
})();
