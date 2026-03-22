// theme-colors.js — centralized theme color reader for vanilla JS modules.
// Reads CSS custom properties once, caches the result, and invalidates on
// color-scheme change so the next access picks up dark-mode values.
(function () {
  "use strict";

  let cache = null;

  function read() {
    const s = getComputedStyle(document.documentElement);
    const get = function (k) { return s.getPropertyValue(k).trim(); };
    cache = {
      accent:         get("--accent"),
      statusOk:       get("--status-ok"),
      statusWarn:     get("--status-warn"),
      statusError:    get("--status-error"),
      statusInfo:     get("--status-info"),
      statusEarlyDep: get("--status-early-dep"),
      statusMuted:    get("--status-muted"),
      surfaceDark:    get("--surface-dark"),
      textColor:      get("--pico-color"),
      termAccent:     get("--term-accent"),
    };
    return cache;
  }

  if (window.matchMedia) {
    window.matchMedia("(prefers-color-scheme: dark)")
      .addEventListener("change", function () { cache = null; });
  }

  window.ThemeColors = function () { return cache || read(); };

  // Platform detection — cached, reusable across all modules
  let _isTouch = null;
  window.isTouch = function () {
    if (_isTouch === null) _isTouch = 'ontouchstart' in window || navigator.maxTouchPoints > 0;
    return _isTouch;
  };
  window.isMobile = function () {
    return window.innerWidth <= 768;
  };

  // Theme picker: "light", "dark", or "system" (default)
  // Persists choice in localStorage. "system" removes data-theme so
  // prefers-color-scheme media query takes over.
  window.setTheme = function (mode) {
    var root = document.documentElement;
    // Enable transition class for smooth crossfade
    root.classList.add("theme-transition");
    if (mode === "system") {
      root.removeAttribute("data-theme");
      localStorage.setItem("theme", "system");
    } else {
      root.setAttribute("data-theme", mode);
      localStorage.setItem("theme", mode);
    }
    cache = null;
    updateThemePicker(mode);
    // Remove transition class after animation completes
    setTimeout(function () { root.classList.remove("theme-transition"); }, 500);
  };

  // Console helper (backwards compat)
  window.toggleTheme = function (mode) {
    if (mode) { window.setTheme(mode); return; }
    var current = document.documentElement.getAttribute("data-theme");
    window.setTheme(current === "dark" ? "light" : "dark");
  };

  function updateThemePicker(active) {
    var btns = document.querySelectorAll(".theme-pick");
    for (var i = 0; i < btns.length; i++) {
      var b = btns[i];
      var isActive = b.getAttribute("data-theme-pick") === active;
      b.classList.toggle("active", isActive);
      b.setAttribute("aria-checked", isActive ? "true" : "false");
    }
  }

  // Apply saved preference on load (runs before DOMContentLoaded to avoid flash)
  var saved = localStorage.getItem("theme") || "system";
  if (saved === "light" || saved === "dark") {
    document.documentElement.setAttribute("data-theme", saved);
  }

  // Wire up picker buttons once DOM is ready
  document.addEventListener("DOMContentLoaded", function () {
    updateThemePicker(saved);
    document.addEventListener("click", function (e) {
      var btn = e.target.closest(".theme-pick");
      if (!btn) return;
      var mode = btn.getAttribute("data-theme-pick");
      if (mode) window.setTheme(mode);
    });
  });
})();
