// Runs before styles load so a saved override never flashes the other theme.
(function () {
  'use strict';
  var root = document.documentElement;
  var name = (document.querySelector('meta[name="app-name"]') || {}).content || 'tycswap';
  var key = name + '_appearance';
  function normalize(value) { return value === 'light' || value === 'dark' ? value : 'auto'; }
  var mode = 'auto';
  try {
    var cookie = document.cookie.split('; ').filter(function (part) { return part.indexOf(key + '=') === 0; })[0];
    mode = normalize(cookie ? cookie.slice(key.length + 1) : 'auto');
  } catch (e) { /* Cookies are optional. */ }
  function apply(value) {
    mode = normalize(value);
    if (mode === 'auto') { root.removeAttribute('data-theme'); }
    else { root.setAttribute('data-theme', mode); }
    var picker = document.getElementById('appearance-theme');
    if (picker) { picker.value = mode; }
  }
  apply(mode);
  document.addEventListener('DOMContentLoaded', function () {
    var picker = document.getElementById('appearance-theme');
    if (!picker) { return; }
    apply(mode);
    picker.addEventListener('change', function () {
      apply(picker.value);
      try {
        document.cookie = key + '=' + mode + '; Path=/; SameSite=Strict; Max-Age=' + (mode === 'auto' ? '0' : '31536000');
      } catch (e) { /* The selected mode still works for this page. */ }
    });
  });
}());
