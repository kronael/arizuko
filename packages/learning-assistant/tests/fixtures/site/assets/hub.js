/**
 * hub.js - Reusable knowledge hub JavaScript
 * Features: theme toggle, nav highlighting, back-to-top, back navigation
 * Usage: <script src="../assets/hub.js"></script> (adjust ../ to the page depth)
 */

(function() {
  'use strict';

  // ==================== UTILS ====================
  function isHubStyled() {
    return !!document.querySelector('link[href*="hub.css"]');
  }

  // ==================== THEME TOGGLE ====================
  function initTheme() {
    if (!isHubStyled()) return;
    var saved = localStorage.getItem('theme');
    var prefersDark = window.matchMedia('(prefers-color-scheme: dark)').matches;
    var theme = saved || (prefersDark ? 'dark' : 'light');
    document.documentElement.setAttribute('data-theme', theme);
    updateThemeButton(theme);
  }

  function toggleTheme() {
    var current = document.documentElement.getAttribute('data-theme') || 'dark';
    var next = current === 'dark' ? 'light' : 'dark';
    document.documentElement.setAttribute('data-theme', next);
    localStorage.setItem('theme', next);
    updateThemeButton(next);
  }

  function updateThemeButton(theme) {
    var btn = document.querySelector('.theme-toggle');
    if (btn) {
      btn.textContent = theme === 'dark' ? '🔆' : '🌙';
      btn.setAttribute('aria-label', theme === 'dark' ? 'Switch to light mode' : 'Switch to dark mode');
    }
  }

  // ==================== NAVIGATION HIGHLIGHTING ====================
  function initNavHighlight() {
    var sections = document.querySelectorAll('section[id]');
    var navItems = document.querySelectorAll('.nav-item');

    if (sections.length === 0 || navItems.length === 0) return;

    function updateNav() {
      var current = '';
      sections.forEach(function(s) {
        if (window.scrollY >= s.offsetTop - 120) {
          current = s.id;
        }
      });
      navItems.forEach(function(n) {
        var href = n.getAttribute('href');
        n.classList.toggle('active', href === '#' + current);
      });
    }

    window.addEventListener('scroll', updateNav, { passive: true });
    updateNav();
  }

  // ==================== BACK TO TOP ====================
  function initBackToTop() {
    const btn = document.querySelector('.back-to-top');
    if (!btn) return;

    function updateVisibility() {
      var visible = window.scrollY > 400 || document.activeElement === btn;
      btn.disabled = !visible;
      btn.classList.toggle('visible', visible);
    }

    window.addEventListener('scroll', updateVisibility, { passive: true });
    btn.addEventListener('blur', updateVisibility);
    btn.addEventListener('click', function() {
      var reduced = window.matchMedia('(prefers-reduced-motion: reduce)').matches;
      window.scrollTo({ top: 0, behavior: reduced ? 'auto' : 'smooth' });
    });
    updateVisibility();
  }

  // ==================== THEME BUTTON ====================
  function createThemeButton() {
    var btn = document.querySelector('.theme-toggle');
    if (btn) {
      btn.addEventListener('click', toggleTheme);
      // Set initial icon
      var theme = document.documentElement.getAttribute('data-theme') || 'dark';
      updateThemeButton(theme);
      return;
    }
    if (!isHubStyled()) return;

    btn = document.createElement('button');
    btn.className = 'theme-toggle';
    btn.addEventListener('click', toggleTheme);
    document.body.appendChild(btn);
    // Set initial icon for created button
    var theme = document.documentElement.getAttribute('data-theme') || 'dark';
    updateThemeButton(theme);
  }

  // ==================== BACK TO TOP BUTTON ====================
  function createBackToTopButton() {
    if (document.querySelector('.back-to-top')) return;
    if (!isHubStyled()) return;

    const btn = document.createElement('button');
    btn.className = 'back-to-top';
    btn.textContent = '\u2191 top';
    document.body.appendChild(btn);
  }

  // ==================== BACK NAVIGATION ====================
  function getBackUrl() {
    // 1. Existing back button
    var btn = document.querySelector('.back-btn');
    if (btn) return btn.getAttribute('href');

    // 2. Site-nav brand link
    var brand = document.querySelector('.site-nav-brand');
    if (brand) return brand.getAttribute('href');

    // 3. Compute parent from path
    var path = window.location.pathname.replace(/\/+$/, '');
    if (!path) return null;
    var parts = path.split('/').filter(Boolean);
    if (parts.length === 0) return null;
    parts.pop();
    return '/' + (parts.length ? parts.join('/') + '/' : '');
  }

  function createBackButton() {
    // Skip if page already has navigation
    if (document.querySelector('.back-btn') || document.querySelector('.site-nav')) return;

    var url = getBackUrl();
    if (!url) return;

    var btn = document.createElement('a');
    btn.className = 'back-btn';
    btn.href = url;
    btn.innerHTML = '&larr; back';
    document.body.insertBefore(btn, document.body.firstChild);

    // Inject minimal styles when hub.css isn't loaded
    if (!isHubStyled()) {
      var s = document.createElement('style');
      s.textContent = [
        '.back-btn{position:fixed;top:1em;left:1em;',
        'background:rgba(20,20,25,0.85);',
        'border:1px solid rgba(255,255,255,0.12);border-radius:6px;',
        'padding:.5em .8em;font-size:1em;z-index:1000;',
        'color:rgba(255,255,255,0.7);text-decoration:none;',
        'font-family:system-ui,-apple-system,sans-serif;',
        'transition:all .2s;',
        'backdrop-filter:blur(8px);-webkit-backdrop-filter:blur(8px)}',
        '.back-btn:hover{color:#6af;border-color:#6af;text-decoration:none}',
        '@media(max-width:600px){.back-btn{display:none}}'
      ].join('');
      document.head.appendChild(s);
    }
  }

  function initMouseBack() {
    // Mouse button 3 = "back" on 5-button mice
    document.addEventListener('mousedown', function(e) {
      if (e.button === 3) {
        var url = getBackUrl();
        if (url) {
          e.preventDefault();
          e.stopPropagation();
          window.location.href = url;
        }
      }
    }, true);
  }

  // ==================== POST-RENDER MARKDOWN FIXES ====================
  function fixMarkdownContent() {
    var content = document.getElementById('content');
    if (!content) return;

    content.querySelectorAll('table').forEach(function(table) {
      if (table.parentElement.classList.contains('table-scroll')) return;
      var wrapper = document.createElement('div');
      wrapper.className = 'table-scroll';
      table.parentNode.insertBefore(wrapper, table);
      wrapper.appendChild(table);
    });
  }

  // ==================== ACCORDION BEHAVIOR ====================
  function initAccordion() {
    // Collapse other <details> in same timeline-item when one opens
    document.querySelectorAll('.timeline-item details').forEach(function(details) {
      details.addEventListener('toggle', function() {
        if (!this.open) return;

        // Find parent timeline-item
        var parent = this.closest('.timeline-item');
        if (!parent) return;

        // Close all other details in same timeline-item
        parent.querySelectorAll('details').forEach(function(other) {
          if (other !== details && other.open) {
            other.open = false;
          }
        });
      });
    });
  }

  // ==================== EXTERNAL LINKS ====================
  // External anchors open in a new tab and don't leak/endorse:
  // target=_blank + rel="noopener noreferrer nofollow". Their trailing
  // right-arrow (→) becomes an up-right arrow (↗) to signal "opens away".
  // Last non-empty descendant text node of an element.
  function lastTextNode(el) {
    var last = null;
    try {
      var w = document.createTreeWalker(el, NodeFilter.SHOW_TEXT, null);
      while (w.nextNode()) { if (w.currentNode.textContent.trim()) last = w.currentNode; }
    } catch (e) { return null; }
    return last;
  }

  // One arrow, exactly: external links (new tab) get a single trailing ↗;
  // internal links (same tab) are plain, no arrow. Only trailing arrows are
  // touched — mid-text glyphs like FASTQ→VCF are left alone.
  function initLinkArrows() {
    var host = location.hostname;
    document.querySelectorAll('a[href]').forEach(function (a) {
      var href = a.getAttribute('href') || '';
      if (/^(#|mailto:|tel:|javascript:)/i.test(href)) return;
      if (a.dataset.arrowed) return;
      var ext;
      try {
        var u = new URL(a.href, location.href);
        ext = !!u.hostname && u.hostname !== host;
      } catch (e) { return; }
      a.dataset.arrowed = '1';
      var tn = lastTextNode(a);
      if (tn) tn.textContent = tn.textContent.replace(/[\s]*[→➔➡↗»]+[\s]*$/, '');
      else if (a.children.length === 0) a.textContent = a.textContent.replace(/[\s]*[→➔➡↗»]+[\s]*$/, '');
      if (ext) {
        a.target = '_blank';
        a.rel = 'noopener noreferrer nofollow';
        if (tn) tn.textContent = tn.textContent.replace(/\s+$/, '') + ' ↗';
        else if (a.children.length === 0) a.textContent = a.textContent.replace(/\s+$/, '') + ' ↗';
      }
    });
  }

  // Research/LLM-authored pages carry one standard disclaimer banner,
  // keyed off <meta name="NS:type" content="research"> or
  // <meta name="NS:authored" content="llm">, where NS is the site's
  // metadata namespace (matched by suffix). Removes any older
  // ad-hoc digest note or callout variant to avoid dupes.
  function initResearchBanner() {
    try {
      var typeMeta = document.querySelector('meta[name$=":type"]');
      var authoredMeta = document.querySelector('meta[name$=":authored"]');
      var isResearch = typeMeta && (typeMeta.getAttribute('content') || '') === 'research';
      var isLlmAuthored = authoredMeta && (authoredMeta.getAttribute('content') || '') === 'llm';
      var container = document.querySelector('.hub-container');
      if (!container) return;
      container.querySelectorAll('.callout, .research-banner').forEach(function (el) {
        if (/living research digest|llm-authored|human-prompted|not human-edited|LLM-assembled digest/i.test(el.textContent || '')) { el.remove(); }
      });
      container.querySelectorAll('.callout').forEach(function (el) {
        if (/machine-assembled|refreshed monthly/i.test(el.textContent || '')) { el.remove(); }
      });
      if (!isResearch && !isLlmAuthored) return;
      if (container.querySelector('.research-banner')) return;
      var b = document.createElement('div');
      b.className = 'research-banner';
      b.innerHTML = '<span class="research-banner-tag">LLM-authored</span> Human-prompted, machine-assembled, unedited.';
      var anchor = container.querySelector('.hub-subtitle') || container.querySelector('h1');
      if (anchor && anchor.parentNode === container) container.insertBefore(b, anchor.nextSibling);
      else container.insertBefore(b, container.firstChild);
    } catch (e) { /* fail open */ }
  }

  // ==================== INIT ====================
  function init() {
    initTheme();
    createBackToTopButton();
    createBackButton();
    createThemeButton(); // Call after theme is initialized
    initNavHighlight();
    initBackToTop();
    initMouseBack();
    fixMarkdownContent();
    initAccordion();
    initCopyLinks();
    initResearchBanner();
    initLinkArrows();
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else {
    init();
  }
})();

// ==================== COPY LINK BUTTONS ====================
function initCopyLinks() {
  var copyBtns = document.querySelectorAll('.copy-link-btn');
  copyBtns.forEach(function(btn) {
    btn.addEventListener('click', function(e) {
      e.preventDefault();
      e.stopPropagation();
      var link = btn.getAttribute('data-link');
      if (!link) return;

      navigator.clipboard.writeText(link).then(function() {
        var originalText = btn.textContent;
        btn.textContent = '✓';
        btn.classList.add('copied');
        setTimeout(function() {
          btn.textContent = originalText;
          btn.classList.remove('copied');
        }, 2000);
      }).catch(function(err) {
        console.error('Failed to copy link:', err);
      });
    });
  });
}
