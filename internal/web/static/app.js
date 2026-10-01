// app.js — tycswap dashboard client (DESIGN A25). Vanilla JS, no build step:
// fetch /api/state once, then follow /api/events (SSE) with the browser's
// built-in reconnect. Countdowns are recomputed client-side every second from
// the server's resets_at / expiresAt / startedAt values. The Auto tab ranks
// "Next best" candidates client-side with the same keys tui/autoview.go uses
// (candidateLessBest / candidateLessSoonest) and colours engine events like
// tui eventColor. Components: tile(), meter(), chip() — one implementation
// each, reused on every tab.
(function () {
  'use strict';

  var CSRF = (document.querySelector('meta[name="csrf"]') || {}).content || '';
  // The command name for the hints this page prints (brand.Name, templated).
  var NAME = (document.querySelector('meta[name="app-name"]') || {}).content || 'tycswap';
  var $ = function (id) { return document.getElementById(id); };
  var state = null;
  var lastStateAt = 0;
  var inflight = {};
  var showTokenStatus = false;
  var strategy = 'best';
  var autoLog = [];      // ring of AutoEventView, oldest first
  var autoLogLastAt = 0;
  var LOG_RING = 200;
  var sliderActive = false;
  var sliderTimer = null;
  // Idle sessions are hidden by default: what matters at a glance is what is
  // working or waiting for you. 'active' = busy + waiting.
  var sessFilter = { q: '', status: 'active', sort: 'busy' };
  var openGroups = {};   // cwd → collapsed? (default open)

  // ---- helpers -------------------------------------------------------------

  // UNSAFE_ATTR: attributes el() never sets. API strings reach the page only
  // as text or as inert attributes, so an account name, a path or a session
  // title can never become a link, a script source, a style or a handler.
  var UNSAFE_ATTR = /^(on|href$|src$|srcdoc$|style$|formaction$|action$|xlink:href$)/i;

  function el(tag, attrs, children) {
    var e = document.createElement(tag);
    if (attrs) {
      Object.keys(attrs).forEach(function (k) {
        if (UNSAFE_ATTR.test(k)) { return; }
        if (k === 'text') { e.textContent = attrs[k]; }
        else if (k === 'class') { e.className = attrs[k]; }
        else if (k === 'checked' || k === 'disabled' || k === 'selected' || k === 'hidden' || k === 'open') { e[k] = !!attrs[k]; }
        else if (attrs[k] !== null && attrs[k] !== undefined) { e.setAttribute(k, attrs[k]); }
      });
    }
    (children || []).forEach(function (c) {
      if (c === null || c === undefined || c === false) { return; }
      e.appendChild(typeof c === 'string' ? document.createTextNode(c) : c);
    });
    return e;
  }

  function clear(node) { while (node.firstChild) { node.removeChild(node.firstChild); } }

  function fmtDur(ms) {
    if (ms === null || isNaN(ms)) { return '—'; }
    if (ms <= 0) { return 'now'; }
    var s = Math.floor(ms / 1000);
    var d = Math.floor(s / 86400), h = Math.floor((s % 86400) / 3600), m = Math.floor((s % 3600) / 60);
    if (d > 0) { return d + 'd ' + h + 'h'; }
    if (h > 0) { return h + 'h ' + (m < 10 ? '0' : '') + m + 'm'; }
    if (m > 0) { return m + 'm'; }
    return '<1m';
  }

  function fmtAgo(ms) {
    if (ms === null || isNaN(ms)) { return '—'; }
    if (ms < 60000) { return 'just now'; }
    return fmtDur(ms) + ' ago';
  }

  function fmtAgoShort(ms) {
    if (ms < 1000) { return 'now'; }
    if (ms < 60000) { return Math.floor(ms / 1000) + 's ago'; }
    return fmtDur(ms) + ' ago';
  }

  function fmtUnix(sec) {
    if (sec === null || sec === undefined) { return '—'; }
    var d = new Date(sec * 1000);
    return isNaN(d.getTime()) ? '—' : d.toLocaleString();
  }

  function fmtClock(sec) {
    var d = new Date(sec * 1000);
    if (isNaN(d.getTime())) { return ''; }
    var hh = d.getHours(), mm = d.getMinutes(), ss = d.getSeconds();
    return (hh < 10 ? '0' : '') + hh + ':' + (mm < 10 ? '0' : '') + mm + ':' + (ss < 10 ? '0' : '') + ss;
  }

  function pctNum(v) {
    var n = typeof v === 'number' ? v : parseFloat(v);
    return isNaN(n) ? null : n;
  }

  function fmtPct(p) {
    if (p === null) { return '—'; }
    if (p > 999) { return '>999%'; }
    if (p < -999) { return '<-999%'; }
    return Math.round(p) + '%';
  }

  function accountName(a) { return a.alias || a.email || ('#' + a.number); }
  // Claude rows are the ones every action targets; other providers' rows
  // (Codex) are shown read-only and numbered in their own slot space.
  function isClaude(a) { return (a.provider || 'claude') === 'claude'; }
  function claudeRows(st) { return ((st && st.accounts) || []).filter(isClaude); }
  // rowKey is the account's API address: every account route takes the row
  // key ("claude:2"), never a bare slot, because slot numbers are per provider.
  function rowKey(a) { return a.key || ((a.provider || 'claude') + ':' + a.number); }
  function keyPath(a) { return encodeURIComponent(rowKey(a)); }
  function accountLabel(a) {
    var parts = [a.alias, a.email].filter(Boolean);
    return '#' + a.number + (parts.length ? ' ' + parts.join(' \u00b7 ') : '');
  }

  function toast(msg, kind) {
    var box = $('toasts');
    var t = el('div', { class: 'toast ' + (kind === 'ok' ? 'toast-ok' : 'toast-err'), role: kind === 'ok' ? 'status' : 'alert', text: msg });
    box.appendChild(t);
    setTimeout(function () { if (t.parentNode) { t.parentNode.removeChild(t); } }, kind === 'ok' ? 3500 : 7000);
  }

  function setConn(status) {
    var dot = $('conn-dot'), txt = $('conn-text');
    dot.className = 'dot ' + (status === 'on' ? 'dot-on' : status === 'wait' ? 'dot-wait' : 'dot-off');
    txt.textContent = status === 'on' ? 'live' : status === 'wait' ? 'reconnecting…' : 'disconnected';
  }

  // api performs an authenticated JSON request; resolves with the parsed body
  // or rejects with an Error carrying the server's message.
  function api(method, url, body) {
    var opts = { method: method, credentials: 'same-origin', headers: { 'X-CSRF-Token': CSRF, 'Accept': 'application/json' } };
    if (body !== undefined) {
      opts.headers['Content-Type'] = 'application/json';
      opts.body = JSON.stringify(body);
    }
    return fetch(url, opts).then(function (res) {
      return res.text().then(function (txt) {
        var data = null;
        try { data = txt ? JSON.parse(txt) : null; } catch (e) { data = null; }
        if (res.status === 401) { throw new Error('Session expired — run ' + NAME + ' web again and open the URL it prints.'); }
        if (!res.ok) { throw new Error((data && data.error) || ('HTTP ' + res.status)); }
        return data;
      });
    });
  }

  // run wraps a button-triggered request: disables the button, toasts.
  function run(btn, label, promise) {
    if (btn) { btn.disabled = true; btn.setAttribute('aria-busy', 'true'); }
    return promise.then(function () {
      toast(label + ' done.', 'ok');
    }).catch(function (err) {
      toast(err && err.message ? err.message : String(err));
    }).then(function () {
      if (btn) { btn.disabled = false; btn.removeAttribute('aria-busy'); }
    });
  }

  // ---- components ------------------------------------------------------------

  // chip(text, variant) → status/kind chip. Variants: active, accent, good,
  // warn, serious, crit, outline, kind, '' (neutral).
  function chip(text, variant, title, withDot) {
    var c = el('span', { class: 'chip' + (variant ? ' chip-' + variant : ''), title: title || null }, [
      withDot ? el('span', { class: 'dot', 'aria-hidden': 'true' }) : null, text
    ]);
    return c;
  }

  // tile(label, value, sub, tone) → stat tile (proportional figures on value).
  function tile(label, value, sub, tone) {
    var t = el('div', { class: 'tile' + (tone ? ' t-' + tone : '') }, [
      el('div', { class: 'tile-label', text: label }),
      typeof value === 'string' ? el('div', { class: 'tile-value' + (value.length > 14 ? ' small' : ''), text: value }) : el('div', { class: 'tile-value' }, [value])
    ]);
    if (sub) { t.appendChild(typeof sub === 'string' ? el('div', { class: 'tile-sub', text: sub }) : el('div', { class: 'tile-sub' }, Array.isArray(sub) ? sub : [sub])); }
    return t;
  }

  function band(pct) {
    if (pct === null) { return 'none'; }
    if (pct >= 100) { return 'crit'; }
    if (pct >= 90) { return 'hi'; }
    if (pct >= 70) { return 'warn'; }
    return 'ok';
  }

  // meter({label, pct, resetsAt, size, foot}) → label · value on top, tone-on-
  // tone track, reset countdown underneath. pct null → dashed "no data".
  function meter(o) {
    var pct = pctNum(o.pct);
    var b = band(pct);
    var m = el('div', { class: 'meter band-' + b + (o.size ? ' ' + o.size : '') });
    m.appendChild(el('div', { class: 'meter-head' }, [
      el('span', { class: 'meter-label', text: o.label, title: o.label }),
      el('span', { class: 'meter-val', text: fmtPct(pct) })
    ]));
    var fill = el('div', { class: 'meter-fill' });
    fill.style.width = (pct === null ? 0 : Math.max(0, Math.min(100, pct))) + '%';
    m.appendChild(el('div', { class: 'meter-track', role: 'meter', 'aria-valuemin': '0', 'aria-valuemax': '100', 'aria-valuenow': pct === null ? '0' : String(Math.round(Math.max(0, Math.min(100, pct)))), 'aria-valuetext': pct === null ? 'no data' : fmtPct(pct) + ' used', 'aria-label': o.label + ' used' }, [fill]));
    if (o.foot !== false) {
      var foot = el('div', { class: 'meter-foot' + (o.size === 'compact' ? ' short' : '') });
      if (o.resetsAt) {
        foot.setAttribute('data-resets', o.resetsAt);
        foot.textContent = countdownText(o.resetsAt, o.size === 'compact');
        if (o.size === 'compact') { foot.title = 'resets in ' + fmtDur(Date.parse(o.resetsAt) - Date.now()); }
      }
      else if (o.footText) { foot.textContent = o.footText; }
      else if (pct === null) { foot.textContent = 'no data'; }
      m.appendChild(foot);
    }
    return m;
  }

  function emptyMeter(label, size) { return meter({ label: label, pct: null, size: size }); }

  // countdownText renders "resets 2h 13m" (or the bare "2h 13m" in short form
  // for compact meters, where the column header already says what resets).
  function countdownText(iso, short) {
    if (!iso) { return ''; }
    var t = Date.parse(iso);
    if (isNaN(t)) { return ''; }
    var d = fmtDur(t - Date.now());
    return short ? (d === 'now' ? 'resets now' : '↻ ' + d) : 'resets ' + d;
  }

  function tickCountdowns() {
    var nodes = document.querySelectorAll('[data-resets]');
    for (var i = 0; i < nodes.length; i++) {
      var iso = nodes[i].getAttribute('data-resets');
      if (iso) { nodes[i].textContent = countdownText(iso, nodes[i].classList.contains('short')); }
    }
    var ago = document.querySelectorAll('[data-started]');
    for (var k = 0; k < ago.length; k++) {
      var ms = parseInt(ago[k].getAttribute('data-started'), 10);
      ago[k].textContent = ms > 0 ? fmtAgo(Date.now() - ms) : '—';
    }
    if (lastStateAt) { $('conn-age').textContent = '· updated ' + fmtAgoShort(Date.now() - lastStateAt); }
  }

  // ---- tabs ----------------------------------------------------------------

  var tabs = Array.prototype.slice.call(document.querySelectorAll('[role="tab"]'));
  function selectTab(name, focus) {
    tabs.forEach(function (t) {
      var on = t.getAttribute('data-tab') === name;
      t.setAttribute('aria-selected', on ? 'true' : 'false');
      t.tabIndex = on ? 0 : -1;
      var panel = $('panel-' + t.getAttribute('data-tab'));
      if (panel) { panel.hidden = !on; }
      if (on && focus) { t.focus(); }
    });
    if (window.location.hash !== '#' + name) {
      try { history.replaceState(null, '', '#' + name); } catch (e) { /* ignore */ }
    }
  }
  tabs.forEach(function (t) {
    t.addEventListener('click', function () { selectTab(t.getAttribute('data-tab')); });
  });
  $('tablist').addEventListener('keydown', function (ev) {
    var i = tabs.indexOf(document.activeElement);
    if (i < 0) { return; }
    var n = null;
    if (ev.key === 'ArrowRight') { n = (i + 1) % tabs.length; }
    else if (ev.key === 'ArrowLeft') { n = (i - 1 + tabs.length) % tabs.length; }
    else if (ev.key === 'Home') { n = 0; }
    else if (ev.key === 'End') { n = tabs.length - 1; }
    if (n !== null) { ev.preventDefault(); selectTab(tabs[n].getAttribute('data-tab'), true); }
  });
  function tabFromHash() {
    var h = (window.location.hash || '').replace('#', '');
    var known = tabs.some(function (t) { return t.getAttribute('data-tab') === h; });
    selectTab(known ? h : 'dashboard');
  }
  tabFromHash();
  window.addEventListener('hashchange', tabFromHash);

  // segmented controls (radiogroup semantics)
  function initSeg(root, onChange) {
    var btns = Array.prototype.slice.call(root.querySelectorAll('button'));
    function set(btn) {
      btns.forEach(function (b) { b.setAttribute('aria-checked', b === btn ? 'true' : 'false'); b.tabIndex = b === btn ? 0 : -1; });
      onChange(btn);
    }
    btns.forEach(function (b) { b.addEventListener('click', function () { set(b); }); });
    root.addEventListener('keydown', function (ev) {
      var i = btns.indexOf(document.activeElement);
      if (i < 0) { return; }
      var n = null;
      if (ev.key === 'ArrowRight' || ev.key === 'ArrowDown') { n = (i + 1) % btns.length; }
      if (ev.key === 'ArrowLeft' || ev.key === 'ArrowUp') { n = (i - 1 + btns.length) % btns.length; }
      if (n !== null) { ev.preventDefault(); set(btns[n]); btns[n].focus(); }
    });
    return { set: function (pred) { var b = btns.filter(pred)[0]; if (b) { set(b); } } };
  }
  initSeg($('strategy-seg'), function (btn) { strategy = btn.getAttribute('data-strategy') || 'best'; });

  // ---- modal ---------------------------------------------------------------

  var modal = $('modal'), modalForm = $('modal-form');
  var modalState = null;

  function supportsDialog() { return typeof modal.showModal === 'function'; }

  function openModal(opts) {
    return new Promise(function (resolve) {
      $('modal-title').textContent = opts.title || '';
      $('modal-message').textContent = opts.message || '';
      $('modal-error').textContent = '';
      var fields = $('modal-fields');
      clear(fields);
      (opts.fields || []).forEach(function (f) {
        var input;
        if (f.type === 'select') {
          input = el('select', { name: f.name, id: 'mf-' + f.name });
          (f.options || []).forEach(function (o) { input.appendChild(el('option', { value: o.value, selected: o.value === f.value, text: o.label })); });
        } else if (f.type === 'checkbox') {
          input = el('input', { type: 'checkbox', name: f.name, id: 'mf-' + f.name, checked: !!f.value });
        } else {
          input = el('input', { type: f.type || 'text', name: f.name, id: 'mf-' + f.name, value: f.value || '', placeholder: f.placeholder || '', autocomplete: 'off', spellcheck: 'false' });
          if (f.required) { input.required = true; }
        }
        var label = el('label', { for: 'mf-' + f.name }, [f.label, input]);
        if (f.hint) { label.appendChild(el('span', { class: 'hint', text: f.hint })); }
        fields.appendChild(label);
      });
      var ok = $('modal-ok');
      ok.textContent = opts.okLabel || 'OK';
      ok.className = 'btn ' + (opts.danger ? 'btn-danger' : 'btn-primary');
      modalState = { opts: opts, resolve: resolve };
      if (supportsDialog()) { modal.showModal(); } else { modal.setAttribute('open', ''); }
      var first = fields.querySelector('input, select');
      if (first) { first.focus(); } else { ok.focus(); }
    });
  }

  function closeModal(result) {
    var st = modalState;
    modalState = null;
    if (supportsDialog()) { if (modal.open) { modal.close(); } } else { modal.removeAttribute('open'); }
    if (st) { st.resolve(result); }
  }

  modalForm.addEventListener('submit', function (ev) {
    ev.preventDefault();
    if (!modalState) { return; }
    var values = {};
    var missing = null;
    (modalState.opts.fields || []).forEach(function (f) {
      var input = $('mf-' + f.name);
      if (!input) { return; }
      if (f.type === 'checkbox') { values[f.name] = !!input.checked; }
      else { values[f.name] = (input.value || '').trim(); }
      if (f.required && !values[f.name] && !missing) { missing = f.label; }
    });
    if (missing) { $('modal-error').textContent = missing + ' is required.'; return; }
    closeModal(values);
  });
  $('modal-cancel').addEventListener('click', function () { closeModal(null); });
  modal.addEventListener('cancel', function (ev) { ev.preventDefault(); closeModal(null); });
  modal.addEventListener('click', function (ev) { if (ev.target === modal) { closeModal(null); } });

  function confirmModal(title, message, okLabel) {
    return openModal({ title: title, message: message, okLabel: okLabel || 'Confirm', danger: true }).then(function (v) { return v !== null; });
  }

  // ---- shared derivations ---------------------------------------------------

  function settingValue(st, key) {
    var list = (st && st.settings) || [];
    for (var i = 0; i < list.length; i++) { if (list[i].key === key) { return list[i].value; } }
    return undefined;
  }

  // engineSetting: the value the RUNNING engine was started with (the Go
  // AutoView.Settings map is keyed "autoswitch.<key>"; bare names are accepted
  // too), falling back to the saved settings.json value when not running.
  function engineSetting(st, names, settingsKey) {
    var a = st.auto || {};
    var s = (a.running && a.settings) || {};
    var keys = [settingsKey].concat(names.map(function (n) { return 'autoswitch.' + n; }), names);
    for (var i = 0; i < keys.length; i++) {
      if (s[keys[i]] !== undefined && s[keys[i]] !== null && s[keys[i]] !== '') { return s[keys[i]]; }
    }
    return settingValue(st, settingsKey);
  }

  // parseModelNames mirrors settings.ParseModelNames: COMMA-separated, never
  // whitespace-separated — a window is called "Fable 5", and splitting on
  // spaces turned that into two names that match nothing, so the dashboard
  // silently stopped counting a limit the engine was counting. Lower-cased
  // here because every comparison in this file is lower-case; Go lower-cases
  // at comparison time instead.
  function parseModelNames(v) {
    if (Array.isArray(v)) { return v.map(function (x) { return String(x).trim().toLowerCase(); }).filter(Boolean); }
    if (typeof v !== 'string' || !v.trim()) { return []; }
    return v.split(',').map(function (x) { return x.trim().toLowerCase(); }).filter(Boolean);
  }

  // relevantWindows mirrors oauth.RelevantWindows: 5h, 7d, then scoped windows
  // matched by the configured model names ("all" matches every scoped window).
  function relevantWindows(usage, models) {
    var out = [];
    if (!usage) { return out; }
    if (usage.fiveHour) { out.push({ label: '5h', pct: pctNum(usage.fiveHour.pct), resetsAt: usage.fiveHour.resetsAt }); }
    if (usage.sevenDay) { out.push({ label: '7d', pct: pctNum(usage.sevenDay.pct), resetsAt: usage.sevenDay.resetsAt }); }
    if (models.length && usage.scoped) {
      var all = models.indexOf('all') >= 0;
      usage.scoped.forEach(function (w) {
        var n = String(w.name || '').toLowerCase();
        if (all || models.indexOf(n) >= 0) { out.push({ label: w.name || 'model', pct: pctNum(w.pct), resetsAt: w.resetsAt, scoped: true }); }
      });
    }
    return out.filter(function (w) { return w.pct !== null; });
  }

  function bindingPct(wins) {
    if (!wins.length) { return null; }
    return wins.reduce(function (m, w) { return w.pct > m ? w.pct : m; }, wins[0].pct);
  }

  function renewalTS(wins) {
    var latest = null;
    wins.forEach(function (w) {
      if (w.label === '5h' || !w.resetsAt) { return; }
      var t = Date.parse(w.resetsAt);
      if (!isNaN(t) && (latest === null || t > latest)) { latest = t; }
    });
    return latest === null ? null : latest / 1000;
  }

  function quarantineMap(auto) {
    var q = auto && auto.quarantine;
    if (!q || typeof q !== 'object') { return {}; }
    return (q.quarantine && typeof q.quarantine === 'object') ? q.quarantine : q;
  }

  function quarantineReason(entry) {
    if (!entry || typeof entry !== 'object') { return typeof entry === 'string' ? entry : ''; }
    return entry.reason || entry.cause || entry.kind || '';
  }

  function quarantineSince(entry) {
    if (!entry || typeof entry !== 'object') { return null; }
    var v = entry.at || entry.since || entry.quarantinedAt || entry.quarantined_at;
    return typeof v === 'number' ? v : null;
  }

  var SENTINEL_STATUSES = { token_expired: 'token expired', api_key: 'api key', keychain_unavailable: 'keychain unavailable', relogin_required: 're-login needed', no_credentials: 'no credentials' };

  // rankCandidates mirrors tui/autoview.go candidatesText: one threshold
  // (autoswitch.threshold, or the running engine's live value) against the
  // binding counted window.
  function rankCandidates(st) {
    var auto = st.auto || {};
    var models = parseModelNames(engineSetting(st, ['model', 'Model', 'models'], 'autoswitch.model'));
    var strat = engineSetting(st, ['strategy', 'Strategy'], 'autoswitch.strategy') || 'best';
    var threshold = pctNum(settingValue(st, 'autoswitch.threshold')) || 90;
    if (auto.running && typeof auto.threshold === 'number') { threshold = auto.threshold; }
    var quarantine = quarantineMap(auto);
    var ranked = [];
    claudeRows(st).forEach(function (a) {
      if (a.isActive || !a.rotationEligible) { return; }
      var num = String(a.number);
      var r = { account: a, number: num, bestKey: 0, tier: 0, pct: 0, renewal: null, label: '', windows: [] };
      if (Object.prototype.hasOwnProperty.call(quarantine, num)) {
        var reason = quarantineReason(quarantine[num]);
        r.label = reason ? 'quarantined (' + reason + ')' : 'quarantined';
        r.bestKey = 997; r.tier = 4;
      } else if (a.usageStatus && a.usageStatus !== 'ok' && SENTINEL_STATUSES[a.usageStatus]) {
        r.label = SENTINEL_STATUSES[a.usageStatus]; r.bestKey = 998; r.tier = 5;
      } else {
        var wins = relevantWindows(a.usage, models);
        var pct = bindingPct(wins);
        if (pct === null) {
          r.label = 'usage unknown'; r.bestKey = 999; r.tier = 6;
        } else {
          r.windows = wins; r.bestKey = pct; r.pct = pct; r.renewal = renewalTS(wins);
          if (pct >= 100) { r.tier = 3; }
          else if (pct >= threshold) { r.tier = 2; }
          else if (r.renewal !== null) { r.tier = 0; }
          else { r.tier = 1; }
        }
      }
      ranked.push(r);
    });
    var less = strat === 'soonest-reset' ? lessSoonest : lessBest;
    ranked.sort(function (a, b) { return less(a, b) ? -1 : less(b, a) ? 1 : 0; });
    return { ranked: ranked, models: models, strategy: strat, threshold: threshold };
  }

  function numLess(a, b) { var x = parseInt(a, 10), y = parseInt(b, 10); if (!isNaN(x) && !isNaN(y)) { return x < y; } return a < b; }

  function lessBest(a, b) {
    if (a.bestKey !== b.bestKey) { return a.bestKey < b.bestKey; }
    return numLess(a.number, b.number);
  }

  function lessSoonest(a, b) {
    if (a.tier !== b.tier) { return a.tier < b.tier; }
    switch (a.tier) {
      case 0: if (a.renewal !== b.renewal) { return a.renewal < b.renewal; } break;
      case 1: case 2: if (a.pct !== b.pct) { return a.pct < b.pct; } break;
      case 3:
        if ((a.renewal !== null) !== (b.renewal !== null)) { return a.renewal !== null; }
        if (a.renewal !== null && b.renewal !== null && a.renewal !== b.renewal) { return a.renewal < b.renewal; }
        if (a.pct !== b.pct) { return a.pct < b.pct; }
        break;
    }
    return numLess(a.number, b.number);
  }

  function countingNote(models) {
    if (!models.length) { return 'counting 5h + 7d only'; }
    if (models.indexOf('all') >= 0) { return 'counting 5h + 7d + every model window'; }
    return 'counting 5h + 7d + ' + models.join(', ');
  }

  // modelWindowNames lists the per-model weekly windows the accounts report
  // (e.g. "Fable"), so the UI can say which limits are being ignored.
  function modelWindowNames(st) {
    var names = {};
    claudeRows(st).forEach(function (a) {
      var u = a && a.usage;
      var scoped = (u && (u.scoped || u.models || u.perModel)) || [];
      if (Array.isArray(scoped)) { scoped.forEach(function (w) { if (w && w.name) { names[w.name] = true; } }); }
      else if (scoped && typeof scoped === 'object') { Object.keys(scoped).forEach(function (k) { names[k] = true; }); }
    });
    return Object.keys(names).sort();
  }

  // ignoredModelsNote is the warning shown wherever a ranking is displayed
  // while model windows exist but autoswitch.model is unset: an account at
  // 100% of a model's weekly limit then still counts as eligible.
  function ignoredModelsNote(st, models) {
    if (models.length) { return null; }
    var names = modelWindowNames(st);
    if (!names.length) { return null; }
    return el('span', { class: 'chip chip-warn', title: 'Set autoswitch.model on the Auto tab (a name, a comma-separated list, or all) to count them', text: names.join(', ') + ' limits ignored' });
  }

  // ---- header + summary ------------------------------------------------------

  function renderHeader(st) {
    var v = $('version');
    v.textContent = st.version ? 'v' + st.version : '';
    v.hidden = !st.version;
    var accts = st.accounts || [];
    $('badge-dashboard').textContent = accts.length ? String(accts.length) : '';
    document.title = (st.name && st.name !== 'tycswap' ? st.name : document.title);
    var sess = (st.sessions && st.sessions.claude) || [];
    var badgeS = $('badge-sessions');
    badgeS.textContent = sess.length ? String(sess.length) : '';
    var busy = sess.filter(function (c) { return c.status === 'busy'; }).length;
    badgeS.className = 'tab-badge' + (busy ? ' hot' : '');
    badgeS.title = busy ? busy + ' busy' : '';
    var badgeA = $('badge-auto');
    if (st.auto && st.auto.running) { badgeA.textContent = st.auto.dryRun ? 'dry' : '●'; badgeA.className = 'tab-badge live'; badgeA.title = st.auto.dryRun ? 'engine running (dry-run)' : 'engine running'; }
    else { badgeA.textContent = ''; badgeA.className = 'tab-badge'; }
    renderActiveStrip(st);
  }

  // fallbackNotices: one notice while Claude Code is on an API-key account,
  // which is billed per token. Auto-switch only rotates onto one when
  // autoswitch.includeApiKeyAccounts is on.
  function fallbackNotices(st) {
    var out = [];
    var active = claudeRows(st).filter(function (a) { return a.isActive; })[0];
    if (active && active.kind === 'api_key') {
      out.push(el('div', { class: 'notice notice-crit', role: 'alert' }, [
        el('b', { text: 'Running on API-key account #' + active.number + ' — billed per token. ' }),
        el('span', { text: settingValue(st, 'autoswitch.includeApiKeyAccounts') === true ? 'autoswitch.includeApiKeyAccounts is on, so the engine may keep using it.' : 'Switch to a subscription account when one has room again.' })
      ]));
    }
    return out;
  }

  // renderActiveStrip: the header's always-visible answer to "which account am
  // I on and how much room is left" — the active Claude account and its 5h,
  // 7d and per-model windows, the same meters the account table uses.
  function renderActiveStrip(st) {
    var box = $('hdr-acct');
    if (!box) { return; }
    var accts = claudeRows(st);
    var active = accts.filter(function (a) { return a.isActive; })[0];
    var sig = JSON.stringify(active ? [active.number, active.email, active.alias, active.usage, active.usageStatus, active.atLimit] : null);
    if (box.getAttribute('data-sig') === sig) { return; }
    box.setAttribute('data-sig', sig);
    clear(box);
    if (!active) {
      box.appendChild(el('span', { class: 'hdr-none muted', text: 'no active account' }));
      return;
    }
    var who = el('div', { class: 'hdr-who' }, [
      el('span', { class: 'slot-chip', text: '#' + active.number }),
      el('span', { class: 'hdr-name ellipsis', text: accountName(active), title: active.email || '' })
    ]);
    if (active.atLimit) { who.appendChild(chip('at limit', 'crit')); }
    if (active.kind === 'api_key') { who.appendChild(chip('API key · billed per token', 'crit', 'Claude Code is on a managed API-key account')); }
    box.appendChild(who);
    var u = active.usage || {};
    var meters = el('div', { class: 'hdr-meters' });
    meters.appendChild(u.fiveHour ? meter({ label: '5h', pct: u.fiveHour.pct, resetsAt: u.fiveHour.resetsAt, size: 'compact' }) : emptyMeter('5h', 'compact'));
    meters.appendChild(u.sevenDay ? meter({ label: '7d', pct: u.sevenDay.pct, resetsAt: u.sevenDay.resetsAt, size: 'compact' }) : emptyMeter('7d', 'compact'));
    var scoped = (u.scoped || []).slice();
    var top = scoped.sort(function (x, y) { return (pctNum(y.pct) || 0) - (pctNum(x.pct) || 0); })[0];
    meters.appendChild(top ? meter({ label: top.name || 'model', pct: top.pct, resetsAt: top.resetsAt, size: 'compact' }) : emptyMeter('model', 'compact'));
    box.appendChild(meters);
  }

  function renderSummary(st) {
    var box = $('summary-tiles');
    clear(box);
    var accts = claudeRows(st);
    var eligible = accts.filter(function (a) { return a.rotationEligible; }).length;
    var active = accts.filter(function (a) { return a.isActive; })[0];
    var atLimit = accts.filter(function (a) { return a.atLimit; });
    var res = rankCandidates(st);
    var best = res.ranked.filter(function (r) { return !r.label && r.tier <= 1; })[0];

    box.appendChild(tile('Accounts', String(accts.length), [eligible + ' eligible for rotation']));
    if (active) {
      box.appendChild(tile('Active account', '#' + active.number, [el('span', { class: 'ellipsis', text: accountName(active), title: active.email })], 'accent'));
    } else {
      box.appendChild(tile('Active account', '—', ['no managed account is live']));
    }
    box.appendChild(tile('At limit', String(atLimit.length), [atLimit.length ? atLimit.map(function (a) { return '#' + a.number; }).join(' ') : 'no window exhausted'], atLimit.length ? 'crit' : 'good'));
    if (best) {
      box.appendChild(tile('Best candidate', '#' + best.number, [el('span', { class: 'ellipsis', text: accountName(best.account) }), chip((100 - best.pct).toFixed(0) + '% headroom', 'good')], 'good'));
    } else {
      box.appendChild(tile('Best candidate', '—', [res.ranked.length ? 'no account is under the threshold' : 'no other account'], res.ranked.length ? 'warn' : ''));
    }
  }

  // ---- accounts ------------------------------------------------------------

  function statusChips(a) {
    var out = [];
    if (a.isActive) { out.push(chip('active', 'active')); }
    if (a.atLimit) { out.push(chip('at limit', 'crit', (a.limitingWindows || []).join(', ') || 'a window is at 100%')); }
    if (a.disabled) { out.push(chip('disabled', 'outline', 'held out of auto-rotation')); }
    if (!a.switchable) { out.push(chip('not switchable', 'warn', 'missing stored credentials or config backup')); }
    if (a.usageStatus && SENTINEL_STATUSES[a.usageStatus] && a.usageStatus !== 'api_key') { out.push(chip(SENTINEL_STATUSES[a.usageStatus], 'serious')); }
    if (a.usageStatus === 'unavailable') { out.push(chip('usage unavailable', 'warn', 'the last measurement is stale or failed; the meters show the last good values')); }
    return out;
  }

  function menuButton(label, attrs) {
    var a = { type: 'button', class: 'btn btn-sm' + (attrs.danger ? ' btn-danger' : ''), text: label, role: 'menuitem' };
    Object.keys(attrs).forEach(function (k) { if (k !== 'danger') { a[k] = attrs[k]; } });
    return el('button', a);
  }

  function modelMeter(u, size) {
    if (u && u.scoped && u.scoped.length) {
      // the binding scoped window (highest pct) gets the column; others tooltip
      var top = u.scoped.slice().sort(function (x, y) { return (pctNum(y.pct) || 0) - (pctNum(x.pct) || 0); })[0];
      var m = meter({ label: top.name || 'model', pct: top.pct, resetsAt: top.resetsAt, size: size });
      if (u.scoped.length > 1) { m.title = u.scoped.map(function (w) { return (w.name || 'model') + ' ' + fmtPct(pctNum(w.pct)); }).join(' · '); }
      return m;
    }
    return emptyMeter('model', size);
  }

  function renderAccounts(st) {
    var body = $('accounts-body');
    clear(body);
    var list = st.accounts || [];
    var nClaude = claudeRows(st).length;
    var multi = list.some(function (a) { return !isClaude(a); });
    list = claudeRows(st);
    $('accounts-empty').hidden = list.length > 0;
    $('accounts-tbl').hidden = list.length === 0;
    $('accounts-sub').textContent = nClaude + (nClaude === 1 ? ' account' : ' accounts') + (st.activeNumber !== null && st.activeNumber !== undefined ? ' · active #' + st.activeNumber : ' · none active') + (multi ? ' · other providers are listed with ' + NAME + ' codex list' : '');
    var accIgnored = ignoredModelsNote(st, parseModelNames(settingValue(st, 'autoswitch.model')));
    if (accIgnored) { $('accounts-sub').appendChild(document.createTextNode(' · ')); $('accounts-sub').appendChild(accIgnored); }
    var tokenCol = document.querySelector('.col-token');
    if (tokenCol) { tokenCol.hidden = !showTokenStatus; }

    list.forEach(function (a) {
      var tr = el('tr', { class: (a.isActive ? 'active-row ' : '') + (a.disabled ? 'disabled-row' : ''), 'data-key': rowKey(a) });
      tr.appendChild(el('td', { class: 'c-slot' }, [el('span', { class: 'slot-chip', text: '#' + a.number, 'aria-label': (a.provider || 'claude') + ' slot ' + a.number })]));

      var acct = el('div', { class: 'acct' });
      var nameRow = el('div', { class: 'acct-name' }, [el('span', { class: 'primary', text: a.alias || a.email || '(no email)', title: a.email || '' })]);
      statusChips(a).forEach(function (c) { nameRow.appendChild(c); });
      acct.appendChild(nameRow);
      var meta = el('div', { class: 'acct-meta' });
      if (a.kind) { meta.appendChild(chip(a.kind === 'api_key' ? 'api key' : a.kind, 'kind')); }
      if (a.alias && a.email) { meta.appendChild(el('span', { class: 'org', text: a.email, title: a.email })); }
      if (a.orgName) { meta.appendChild(el('span', { class: 'org', text: a.orgName, title: a.orgName })); }
      acct.appendChild(meta);
      tr.appendChild(el('td', { class: 'c-acct' }, [acct]));

      var u = a.usage || null;
      tr.appendChild(el('td', { class: 'c-meter c-m5' }, [u && u.fiveHour ? meter({ label: '5h', pct: u.fiveHour.pct, resetsAt: u.fiveHour.resetsAt }) : emptyMeter('5h')]));
      tr.appendChild(el('td', { class: 'c-meter c-m7' }, [u && u.sevenDay ? meter({ label: '7d', pct: u.sevenDay.pct, resetsAt: u.sevenDay.resetsAt }) : emptyMeter('7d')]));
      tr.appendChild(el('td', { class: 'c-meter c-mm' }, [modelMeter(u)]));
      if (showTokenStatus) {
        tr.appendChild(el('td', { class: 'c-token token-cell', text: a.tokenStatus || '—' }));
      }

      var acts = el('div', { class: 'row-actions' });
      var sw = el('button', { type: 'button', class: 'btn btn-sm btn-primary', 'data-label': 'Switch', 'aria-label': 'Switch to account ' + a.number, text: a.isActive ? 'Active' : 'Switch' });
      sw.setAttribute('data-post', '/api/switch/' + keyPath(a));
      if (a.isActive || !a.switchable) { sw.disabled = true; }
      acts.appendChild(sw);

      var menu = el('details', { class: 'menu' });
      menu.appendChild(el('summary', { class: 'btn btn-sm btn-icon', 'aria-label': 'More actions for account ' + a.number, 'aria-haspopup': 'menu', text: '⋯' }));
      var listEl = el('div', { class: 'menu-list', role: 'menu' });
      var k = rowKey(a);
      listEl.appendChild(menuButton('Force switch (no backup)', { 'data-action': 'force-switch', 'data-id': k, 'data-name': accountLabel(a), disabled: a.isActive }));
      if (a.disabled) {
        listEl.appendChild(menuButton('Enable for rotation', { 'data-post': '/api/accounts/' + keyPath(a) + '/enable', 'data-label': 'Enable' }));
      } else {
        listEl.appendChild(menuButton('Disable (hold out of rotation)', { 'data-post': '/api/accounts/' + keyPath(a) + '/disable', 'data-label': 'Disable' }));
      }
      listEl.appendChild(el('div', { class: 'menu-sep' }));
      listEl.appendChild(menuButton(a.alias ? 'Change alias…' : 'Set alias…', { 'data-action': 'alias', 'data-id': k, 'data-alias': a.alias || '', 'data-name': accountLabel(a) }));
      listEl.appendChild(menuButton('Move to slot…', { 'data-action': 'move', 'data-id': k, 'data-name': accountLabel(a) }));
      listEl.appendChild(menuButton('Swap with…', { 'data-action': 'swap', 'data-id': k, 'data-name': accountLabel(a) }));
      listEl.appendChild(el('div', { class: 'menu-sep' }));
      listEl.appendChild(menuButton('Remove…', { 'data-action': 'remove', 'data-id': k, 'data-name': accountLabel(a), danger: true }));
      menu.appendChild(listEl);
      acts.appendChild(menu);

      tr.appendChild(el('td', { class: 'c-actions td-actions' }, [acts]));
      body.appendChild(tr);
    });
  }

  document.addEventListener('click', function (ev) {
    document.querySelectorAll('details.menu[open]').forEach(function (d) {
      if (!d.contains(ev.target)) { d.removeAttribute('open'); }
    });
  });
  document.addEventListener('keydown', function (ev) {
    if (ev.key === 'Escape') { document.querySelectorAll('details.menu[open]').forEach(function (d) { d.removeAttribute('open'); }); }
  });

  // ---- sessions ------------------------------------------------------------

  function statusChip(s) {
    var st = s || 'unknown';
    var variant = st === 'busy' ? 'warn' : st === 'waiting' ? 'serious' : st === 'idle' ? 'good' : 'outline';
    return chip(st, variant, null, st !== 'unknown');
  }

  function sessionRank(c) { return c.status === 'busy' ? 0 : c.status === 'waiting' ? 1 : 2; }

  function sortSessions(list) {
    var mode = sessFilter.sort;
    return list.slice().sort(function (a, b) {
      if (mode === 'busy') {
        var r = sessionRank(a) - sessionRank(b);
        if (r !== 0) { return r; }
        return (b.startedAt || 0) - (a.startedAt || 0);
      }
      if (mode === 'newest') { return (b.startedAt || 0) - (a.startedAt || 0); }
      if (mode === 'oldest') { return (a.startedAt || 0) - (b.startedAt || 0); }
      return a.pid - b.pid;
    });
  }

  function sessionRow(c) {
    var tr = el('tr');
    var shortId = (c.sessionId || '').slice(0, 8);
    tr.appendChild(el('td', { class: 'c-title' }, [
      el('span', { class: 'title ellipsis', text: c.title || (shortId ? 'session ' + shortId : '—'), title: c.title || c.sessionId || '' }),
      c.title && shortId ? el('span', { class: 'cell-sub mono', text: shortId }) : null,
      c.profile ? chip('run as #' + c.profile, 'accent', 'started with ' + NAME + ' run / env in the session profile of account #' + c.profile) : null
    ]));
    tr.appendChild(el('td', { class: 'c-pid num mono', text: String(c.pid) }));
    tr.appendChild(el('td', { class: 'c-kind' }, [el('span', { text: c.kind || '—' }), c.entrypoint ? el('span', { class: 'cell-sub', text: ' · ' + c.entrypoint }) : null]));
    tr.appendChild(el('td', { class: 'c-status' }, [statusChip(c.status)]));
    tr.appendChild(el('td', { class: 'c-started cell-sub' }, [el('span', { 'data-started': String(c.startedAt || 0), text: '' })]));
    var stop = el('button', { type: 'button', class: 'btn btn-sm btn-danger', 'data-post': '/api/sessions/' + encodeURIComponent(c.pid) + '/stop', 'data-label': 'Stop', 'data-confirm': 'Stop Claude Code session ' + c.pid + (c.cwd ? ' in ' + c.cwd : '') + '? `claude --continue` in that directory resumes it.', 'aria-label': 'Stop session ' + c.pid, text: 'Stop' });
    tr.appendChild(el('td', { class: 'c-actions td-actions' }, [stop]));
    return tr;
  }

  function lastSegment(p) {
    if (!p) { return '(unknown directory)'; }
    var parts = p.replace(/[\\/]+$/, '').split(/[\\/]/);
    return parts[parts.length - 1] || p;
  }

  function renderSessions(s) {
    var groupsBox = $('sessions-groups');
    clear(groupsBox);
    var list = (s && s.claude) || [];
    var ides = (s && s.ide) || [];
    var counts = { busy: 0, waiting: 0, idle: 0 };
    list.forEach(function (c) { if (counts[c.status] !== undefined) { counts[c.status]++; } });
    var stats = $('sessions-stats');
    clear(stats);
    stats.appendChild(chip(list.length + ' running', 'accent'));
    stats.appendChild(chip(counts.busy + ' busy', counts.busy ? 'warn' : 'outline', null, !!counts.busy));
    stats.appendChild(chip(counts.waiting + ' waiting', counts.waiting ? 'serious' : 'outline', null, !!counts.waiting));
    stats.appendChild(chip(counts.idle + ' idle', 'outline'));

    var q = sessFilter.q.trim().toLowerCase();
    var filtered = list.filter(function (c) {
      if (sessFilter.status === 'active' && c.status === 'idle') { return false; }
      if (sessFilter.status && sessFilter.status !== 'active' && c.status !== sessFilter.status) { return false; }
      if (!q) { return true; }
      return String(c.pid).indexOf(q) >= 0 || (c.cwd || '').toLowerCase().indexOf(q) >= 0 || (c.entrypoint || '').toLowerCase().indexOf(q) >= 0 || (c.kind || '').toLowerCase().indexOf(q) >= 0;
    });
    $('sessions-empty').hidden = list.length > 0;
    document.querySelector('.sess-controls').hidden = list.length === 0;
    if (list.length && !filtered.length) {
      var hint = sessFilter.status === 'active' && !q ? 'All ' + list.length + ' sessions are idle — choose "All statuses" to show them.' : list.length + ' sessions are hidden by the current filter.';
      groupsBox.appendChild(el('div', { class: 'empty-state' }, [el('span', { class: 'title', text: sessFilter.status === 'active' && !q ? 'No active sessions' : 'Nothing matches the filter' }), el('span', { text: hint })]));
    }

    // group by cwd; groups with busy sessions first, then by newest member
    var groups = {};
    filtered.forEach(function (c) { var k = c.cwd || ''; (groups[k] = groups[k] || []).push(c); });
    var keys = Object.keys(groups).sort(function (a, b) {
      var ga = groups[a], gb = groups[b];
      var ra = Math.min.apply(null, ga.map(sessionRank)), rb = Math.min.apply(null, gb.map(sessionRank));
      if (sessFilter.sort === 'busy' && ra !== rb) { return ra - rb; }
      var na = Math.max.apply(null, ga.map(function (c) { return c.startedAt || 0; }));
      var nb = Math.max.apply(null, gb.map(function (c) { return c.startedAt || 0; }));
      if (sessFilter.sort === 'oldest') { return na - nb; }
      if (sessFilter.sort === 'pid') { return Math.min.apply(null, ga.map(function (c) { return c.pid; })) - Math.min.apply(null, gb.map(function (c) { return c.pid; })); }
      return nb - na;
    });
    keys.forEach(function (k) {
      var members = sortSessions(groups[k]);
      var gc = { busy: 0, waiting: 0 };
      members.forEach(function (c) { if (gc[c.status] !== undefined) { gc[c.status]++; } });
      var det = el('details', { class: 'group', open: openGroups[k] !== false });
      det.addEventListener('toggle', function () { openGroups[k] = det.open; });
      var counts2 = el('span', { class: 'gcounts' }, [chip(members.length + (members.length === 1 ? ' session' : ' sessions'), 'outline')]);
      if (gc.busy) { counts2.appendChild(chip(gc.busy + ' busy', 'warn', null, true)); }
      if (gc.waiting) { counts2.appendChild(chip(gc.waiting + ' waiting', 'serious', null, true)); }
      det.appendChild(el('summary', {}, [
        el('span', { class: 'chev', 'aria-hidden': 'true', text: '▶' }),
        // .path is direction:rtl so a long path is cut on the left; the <bdi>
        // keeps the path itself left-to-right, or its punctuation ("/", ".")
        // would be reordered to the wrong end.
        el('span', { class: 'gname' }, [el('b', { text: lastSegment(k), title: k }), el('span', { class: 'path', title: k }, [el('bdi', { text: k || '—' })])]),
        counts2
      ]));
      var tbl = el('table', { class: 'tbl compact sess-tbl' });
      tbl.appendChild(el('thead', {}, [el('tr', {}, [el('th', { scope: 'col', class: 'c-title', text: 'Session' }), el('th', { scope: 'col', class: 'c-pid', text: 'PID' }), el('th', { scope: 'col', class: 'c-kind', text: 'Kind · entrypoint' }), el('th', { scope: 'col', class: 'c-status', text: 'Status' }), el('th', { scope: 'col', class: 'c-started', text: 'Started' }), el('th', { scope: 'col', class: 'c-actions' }, [el('span', { class: 'sr-only', text: 'Actions' })])])]));
      var tb = el('tbody');
      members.forEach(function (c) { tb.appendChild(sessionRow(c)); });
      tbl.appendChild(tb);
      det.appendChild(tbl);
      groupsBox.appendChild(det);
    });

    // IDE table
    var ideBody = $('ide-list');
    clear(ideBody);
    $('ide-count').textContent = ides.length ? String(ides.length) : '';
    $('ide-empty').hidden = ides.length > 0;
    $('ide-tbl').hidden = ides.length === 0;
    ides.slice().sort(function (a, b) { return (a.ideName || '').localeCompare(b.ideName || '') || a.port - b.port; }).forEach(function (i) {
      var tr = el('tr');
      tr.appendChild(el('td', { class: 'cell-main', text: i.ideName || 'IDE' }));
      tr.appendChild(el('td', { class: 'num mono', text: String(i.pid) }));
      tr.appendChild(el('td', { class: 'num mono', text: String(i.port) }));
      var ws = el('td', { class: 'cell-sub' });
      (i.workspaceFolders || []).forEach(function (f, idx) {
        ws.appendChild(el('span', { class: 'mono', title: f }, [el('b', { text: lastSegment(f) }), el('span', { class: 'muted', text: ' ' + f })]));
        if (idx < i.workspaceFolders.length - 1) { ws.appendChild(el('br')); }
      });
      if (!(i.workspaceFolders || []).length) { ws.textContent = '—'; }
      tr.appendChild(ws);
      ideBody.appendChild(tr);
    });
  }

  $('sess-filter').addEventListener('input', function (ev) { sessFilter.q = ev.target.value; if (state) { renderSessions(state.sessions); tickCountdowns(); } });
  $('sess-status').addEventListener('change', function (ev) { sessFilter.status = ev.target.value; if (state) { renderSessions(state.sessions); tickCountdowns(); } });
  $('sess-sort').addEventListener('change', function (ev) { sessFilter.sort = ev.target.value; if (state) { renderSessions(state.sessions); tickCountdowns(); } });

  // ---- auto: render ----------------------------------------------------------

  function renderAuto(st) {
    var a = st.auto;
    var badge = $('auto-badge'), body = $('auto-body');
    clear(body);
    var actions = $('auto-actions');
    var slider = $('threshold-slider');
    var strat = String(engineSetting(st, ['strategy', 'Strategy'], 'autoswitch.strategy') || 'best');
    var models = parseModelNames(engineSetting(st, ['model', 'Model', 'models'], 'autoswitch.model'));
    if (!a) {
      badge.textContent = 'unavailable'; badge.className = 'chip chip-outline';
      body.appendChild(el('div', { class: 'empty-state' }, [el('span', { class: 'title', text: 'Auto-switch is not wired into this build' }), el('code', { text: NAME + ' auto' })]));
      actions.hidden = true;
      slider.disabled = true;
    } else {
      actions.hidden = false;
      if (!a.available) { badge.textContent = 'engine unavailable'; badge.className = 'chip chip-warn'; }
      else if (a.running) { badge.textContent = a.dryRun ? 'running · dry-run' : 'running'; badge.className = 'chip ' + (a.dryRun ? 'chip-warn' : 'chip-good'); }
      else { badge.textContent = 'stopped'; badge.className = 'chip chip-outline'; }

      var tiles = el('div', { class: 'tiles' });
      tiles.appendChild(tile('State', a.running ? (a.dryRun ? 'dry-run' : 'running') : 'stopped', [a.running ? (a.dryRun ? 'decides, never switches' : 'switches near the limit') : 'not polling'], a.running ? (a.dryRun ? 'warn' : 'good') : ''));
      tiles.appendChild(tile('Threshold', Math.round(a.threshold * 10) / 10 + '%', [a.running ? 'live for this run' : 'autoswitch.threshold']));
      tiles.appendChild(tile('Strategy', strat, [el('span', { class: 'ellipsis', text: countingNote(models), title: countingNote(models) })]));
      tiles.appendChild(tile('Started', a.startedAt ? el('span', { 'data-started': String(Math.round(a.startedAt * 1000)), text: fmtAgo(Date.now() - a.startedAt * 1000) }) : '—', [a.startedAt ? fmtUnix(a.startedAt) : 'not running']));
      body.appendChild(tiles);
      fallbackNotices(st).forEach(function (n) { body.appendChild(n); });

      actions.querySelectorAll('[data-action="auto-start"]').forEach(function (b) { b.disabled = !a.available || a.running; });
      actions.querySelector('[data-post="/api/auto/stop"]').disabled = !a.running;
      actions.querySelector('[data-post="/api/auto/wake"]').disabled = !a.running;
      // The live threshold exists only while the engine runs; a stopped
      // engine has nothing to apply it to (the server answers 400).
      slider.disabled = !a.available || !a.running;
      if (!sliderActive) {
        slider.value = String(Math.round(a.threshold));
        $('threshold-out').textContent = Math.round(a.threshold) + '%';
      }
    }

    // quarantine (compact, only when non-empty)
    var qb = $('quarantine-body');
    clear(qb);
    var q = quarantineMap(a);
    var qkeys = Object.keys(q).filter(function (k) { return /^\d+$/.test(k) || (q[k] && typeof q[k] === 'object'); }).sort(function (x, y) { return numLess(x, y) ? -1 : 1; });
    $('quarantine-section').hidden = qkeys.length === 0;
    qkeys.forEach(function (k) {
      var entry = q[k];
      var since = quarantineSince(entry);
      var tr = el('tr');
      tr.appendChild(el('td', {}, [el('span', { class: 'slot-chip', text: '#' + k })]));
      tr.appendChild(el('td', {}, [chip(quarantineReason(entry) || 'quarantined', 'warn')]));
      tr.appendChild(el('td', { class: 'cell-sub' }, [since ? el('span', { 'data-started': String(Math.round(since * 1000)), text: '' }) : '—']));
      qb.appendChild(tr);
    });

    // next best table
    var res = rankCandidates(st);
    var tb = $('nextbest-list');
    clear(tb);
    $('nextbest-sub').textContent = countingNote(res.models) + ' · ' + res.strategy + ' · switch at ' + Math.round(res.threshold * 10) / 10 + '%';
    var nbSub = $('nextbest-sub');
    var savedModels = parseModelNames(settingValue(st, 'autoswitch.model'));
    var ignored = ignoredModelsNote(st, savedModels);
    if (ignored) { nbSub.appendChild(document.createTextNode(' \u00b7 ')); nbSub.appendChild(ignored); }
    if (a && a.running && JSON.stringify(res.models) !== JSON.stringify(savedModels)) {
      nbSub.appendChild(document.createTextNode(' \u00b7 '));
      nbSub.appendChild(chip('running engine: ' + countingNote(res.models), 'outline', 'The engine keeps the settings it was started with; restart it to apply the saved ones'));
    }
    var mlt = $('model-limits-toggle');
    if (mlt && document.activeElement !== mlt) { mlt.checked = savedModels.length > 0; }
    $('model-limits-which').textContent = savedModels.length ? '(' + (savedModels.indexOf('all') >= 0 ? 'all' : savedModels.join(', ')) + ')' : '';
    $('nextbest-empty').hidden = res.ranked.length > 0;
    $('nextbest-tbl').hidden = res.ranked.length === 0;
    res.ranked.forEach(function (r, idx) {
      var tr = el('tr', { class: r.label ? 'nonviable' : '' });
      tr.appendChild(el('td', { class: 'c-rank', text: String(idx + 1) }));
      var acc = r.account;
      var u = acc.usage || null;
      var inlineParts = [];
      if (u && u.fiveHour) { inlineParts.push('5h ' + fmtPct(pctNum(u.fiveHour.pct))); }
      if (u && u.sevenDay) { inlineParts.push('7d ' + fmtPct(pctNum(u.sevenDay.pct))); }
      (u && u.scoped || []).forEach(function (w) { inlineParts.push((w.name || 'model') + ' ' + fmtPct(pctNum(w.pct))); });
      var verdict;
      if (r.tier === 4) { verdict = chip(r.label, 'warn'); }
      else if (r.tier === 5) { verdict = chip(r.label, 'serious'); }
      else if (r.tier === 6) { verdict = chip('usage unknown', 'outline'); }
      else if (r.tier === 3) { verdict = chip('at limit', 'crit'); }
      else if (r.tier === 2) { verdict = chip('at threshold', 'warn'); }
      else { verdict = chip(idx === 0 ? 'next pick' : 'eligible', idx === 0 ? 'good' : 'outline'); }
      var inline = el('div', { class: 'nb-inline cell-sub' }, [verdict.cloneNode(true), el('span', { text: r.label ? '' : (inlineParts.length ? inlineParts.join(' · ') : 'no usage data') })]);
      tr.appendChild(el('td', {}, [el('div', { class: 'acct' }, [
        el('div', { class: 'acct-name' }, [el('span', { class: 'slot-chip', text: '#' + acc.number }), el('span', { class: 'primary', text: accountName(acc), title: acc.email || '' })]),
        inline
      ])]));
      function mini(label, w) { return w ? meter({ label: label, pct: w.pct, resetsAt: w.resetsAt, size: 'compact' }) : emptyMeter(label, 'compact'); }
      tr.appendChild(el('td', { class: 'c-mini' }, [mini('5h', u && u.fiveHour)]));
      tr.appendChild(el('td', { class: 'c-mini' }, [mini('7d', u && u.sevenDay)]));
      var scopedMatch = (u && u.scoped || []).filter(function (w) { return res.models.indexOf('all') >= 0 || res.models.indexOf(String(w.name || '').toLowerCase()) >= 0; });
      var top = scopedMatch.slice().sort(function (x, y) { return (pctNum(y.pct) || 0) - (pctNum(x.pct) || 0); })[0];
      var modelCell;
      if (top) {
        modelCell = meter({ label: top.name || 'model', pct: top.pct, resetsAt: top.resetsAt, size: 'compact' });
      } else if (u && u.scoped && u.scoped.length) {
        modelCell = meter({ label: u.scoped[0].name || 'model', pct: u.scoped[0].pct, resetsAt: u.scoped[0].resetsAt, size: 'compact' });
        modelCell.classList.add('not-counted');
        modelCell.title = (u.scoped[0].name || 'model') + ' is not counted by the engine — add it to autoswitch.model';
      } else {
        modelCell = emptyMeter('model', 'compact');
      }
      tr.appendChild(el('td', { class: 'c-mini' }, [modelCell]));
      tr.appendChild(el('td', { class: 'c-head num strong', text: r.label ? '—' : (100 - r.pct).toFixed(0) + '%' }));
      tr.appendChild(el('td', { class: 'c-verdict' }, [verdict]));
      tb.appendChild(tr);
    });

    if (a && Array.isArray(a.events)) { a.events.forEach(function (ev) { appendAutoEvent(ev, true); }); }
    renderAutoLog();
  }

  // ---- event log -------------------------------------------------------------

  var EVENT_VARIANT = { 'switch': 'accent', 'error': 'warn', 'account-quarantined': 'warn', 'all-exhausted': 'crit', 'account-unquarantined': 'good' };
  var QUIET_KINDS = { 'poll': true, 'no-switch': true, 'sleep': true, 'account-unquarantined': true };
  var logKindFilter = '';

  function appendAutoEvent(ev, fromState) {
    if (!ev || typeof ev !== 'object') { return; }
    var at = typeof ev.at === 'number' ? ev.at : 0;
    if (fromState && at <= autoLogLastAt) { return; }
    if (!fromState && at <= autoLogLastAt) {
      var last = autoLog[autoLog.length - 1];
      if (last && last.at === at && last.kind === ev.kind && last.message === ev.message) { return; }
    }
    autoLog.push(ev);
    if (at > autoLogLastAt) { autoLogLastAt = at; }
    if (autoLog.length > LOG_RING) { autoLog.splice(0, autoLog.length - LOG_RING); }
  }

  function renderAutoLog() {
    var ul = $('auto-log');
    var showQuiet = $('log-quiet').checked;
    var follow = $('log-follow').checked;
    var kinds = {};
    autoLog.forEach(function (ev) { if (ev.kind) { kinds[ev.kind] = true; } });
    var sel = $('log-kind');
    var current = sel.value;
    if (document.activeElement !== sel) { // never rebuild an open dropdown under the pointer
      var want = [''].concat(Object.keys(kinds).sort());
      var have = Array.prototype.map.call(sel.options, function (o) { return o.value; });
      if (want.join('\u0000') !== have.join('\u0000')) {
        clear(sel);
        sel.appendChild(el('option', { value: '', text: 'All kinds' }));
        Object.keys(kinds).sort().forEach(function (k) { sel.appendChild(el('option', { value: k, selected: k === current, text: k })); });
      }
    }
    if (current && !kinds[current]) { logKindFilter = ''; }
    clear(ul);
    var shown = 0;
    autoLog.forEach(function (ev) {
      if (!showQuiet && QUIET_KINDS[ev.kind] && !logKindFilter) { return; }
      if (logKindFilter && ev.kind !== logKindFilter) { return; }
      shown++;
      var variant = EVENT_VARIANT[ev.kind] || (QUIET_KINDS[ev.kind] ? 'outline' : '');
      var li = el('li', {}, [
        el('span', { class: 'stamp', text: ev.at ? fmtClock(ev.at) : '' }),
        el('span', {}, [chip(ev.kind || 'event', variant)]),
        el('span', { class: 'msg' }, [ev.message || '', ev.account ? el('span', { class: 'acct-ref', text: ' · #' + ev.account }) : null])
      ]);
      ul.appendChild(li);
    });
    $('log-count').textContent = autoLog.length ? autoLog.length + (autoLog.length === 1 ? ' event' : ' events') : '';
    $('auto-log-empty').hidden = autoLog.length > 0;
    ul.hidden = shown === 0;
    if (autoLog.length && shown === 0) {
      ul.hidden = false;
      ul.appendChild(el('li', {}, [el('span', { class: 'stamp' }), el('span'), el('span', { class: 'msg muted', text: 'All ' + autoLog.length + ' events are hidden by the filter (quiet kinds: poll, no-switch, sleep).' })]));
    }
    if (follow) { ul.scrollTop = ul.scrollHeight; }
  }
  $('log-quiet').addEventListener('change', renderAutoLog);
  $('log-follow').addEventListener('change', renderAutoLog);
  $('log-kind').addEventListener('change', function (ev) { logKindFilter = ev.target.value; renderAutoLog(); });
  $('log-clear').addEventListener('click', function () { autoLog = []; renderAutoLog(); });

  // threshold slider: live-applied with debounce
  (function () {
    var slider = $('threshold-slider');
    function send() {
      var v = parseInt(slider.value, 10);
      api('POST', '/api/auto/threshold', { threshold: v }).then(function () { toast('Threshold ' + v + '% applied to the running engine.', 'ok'); }).catch(function (err) { toast(err.message); });
    }
    slider.addEventListener('input', function () {
      sliderActive = true;
      $('threshold-out').textContent = slider.value + '%';
      if (sliderTimer) { clearTimeout(sliderTimer); }
      sliderTimer = setTimeout(function () { sliderActive = false; send(); }, 400);
    });
    slider.addEventListener('change', function () {
      if (sliderTimer) { clearTimeout(sliderTimer); sliderTimer = null; }
      sliderActive = false;
      send();
    });
  })();

  // ---- settings (shared row renderer) ---------------------------------------

  var SETTING_LABELS = {
    'autoswitch.threshold': 'Threshold', 'autoswitch.intervalSeconds': 'Poll interval',
    'autoswitch.codexEnabled': 'Codex auto-switch', 'autoswitch.codexThreshold': 'Codex threshold',
    'autoswitch.cooldownSeconds': 'Cooldown', 'autoswitch.hysteresisPct': 'Hysteresis',
    'autoswitch.strategy': 'Strategy', 'autoswitch.includeApiKeyAccounts': 'Include API-key accounts',
    'autoswitch.unhealthyTicks': 'Unhealthy ticks', 'autoswitch.model': 'Model windows'
  };

  function humanLabel(key) {
    if (SETTING_LABELS[key]) { return SETTING_LABELS[key]; }
    var leaf = key.split('.').pop();
    var words = leaf.replace(/([a-z])([A-Z])/g, '$1 $2').replace(/[_-]+/g, ' ').toLowerCase();
    return words.charAt(0).toUpperCase() + words.slice(1);
  }

  function unitFor(key) {
    if (/Seconds$/.test(key)) { return 's'; }
    if (/Pct$|threshold$/i.test(key)) { return '%'; }
    return '';
  }

  function settingControl(sv) {
    var id = 'set-' + sv.key.replace(/[^a-z0-9]/gi, '-');
    var input;
    if (sv.kind === 'bool') {
      input = el('input', { type: 'checkbox', id: id, checked: sv.value === true || sv.value === 'true' });
    } else if (sv.kind === 'choice') {
      input = el('select', { id: id });
      (sv.choices || []).forEach(function (c) { input.appendChild(el('option', { value: c, selected: String(sv.value) === c, text: c })); });
    } else if (sv.kind === 'int' || sv.kind === 'float') {
      input = el('input', { type: 'number', id: id, value: sv.value === null || sv.value === undefined ? '' : String(sv.value), step: sv.kind === 'int' ? '1' : 'any' });
      if (typeof sv.min === 'number') { input.min = String(sv.min); }
      if (typeof sv.max === 'number') { input.max = String(sv.max); }
    } else {
      input = el('input', { type: 'text', id: id, value: sv.value === null || sv.value === undefined ? '' : String(sv.value), autocomplete: 'off', spellcheck: 'false', placeholder: sv.key === 'autoswitch.model' ? 'empty = ignore model limits; all; or names, e.g. Fable, Opus' : '' });
    }
    input.setAttribute('aria-label', humanLabel(sv.key) + ' (' + sv.key + ')');
    return input;
  }

  function controlValue(input, sv) {
    if (sv.kind === 'bool') { return input.checked; }
    if (sv.kind === 'int' || sv.kind === 'float') { var n = parseFloat(input.value); return isNaN(n) ? input.value : n; }
    return input.value;
  }

  function fmtDefault(sv) {
    if (sv.default === null || sv.default === undefined || sv.default === '') { return 'empty'; }
    return String(sv.default) + (typeof sv.default === 'number' ? unitFor(sv.key) : '');
  }

  function settingRow(sv) {
    var row = el('div', { class: 'setting-row', role: 'group', 'aria-label': sv.key });
    var idc = el('div', { class: 'setting-id' }, [
      el('div', { class: 'label-row' }, [el('span', { class: 'label', text: humanLabel(sv.key) }), el('span', { class: 'key', text: sv.key, title: sv.key })]),
      sv.description ? el('div', { class: 'desc', text: sv.description }) : null
    ]);
    row.appendChild(idc);
    var input = settingControl(sv);
    var ctl = el('div', { class: 'setting-ctl' }, [input]);
    var unit = unitFor(sv.key);
    if (typeof sv.min === 'number' || typeof sv.max === 'number') {
      ctl.appendChild(el('span', { class: 'range-note', text: (typeof sv.min === 'number' ? sv.min : '…') + ' – ' + (typeof sv.max === 'number' ? sv.max : '…') + unit }));
    } else if (unit && sv.kind !== 'bool') {
      ctl.appendChild(el('span', { class: 'range-note', text: unit }));
    }
    row.appendChild(ctl);
    row.appendChild(el('div', { class: 'setting-default' }, [
      sv.isDefault ? chip('default', 'outline') : chip('custom', 'accent'),
      el('span', { class: 'def', text: (sv.isDefault ? '' : 'default ') + fmtDefault(sv), title: 'default value' })
    ]));
    var save = el('button', { type: 'button', class: 'btn btn-sm btn-primary', text: 'Save', 'aria-label': 'Save ' + sv.key });
    save.addEventListener('click', function () {
      var value = controlValue(input, sv);
      var go = Promise.resolve(true);
      go.then(function (ok) { if (ok) { run(save, 'Save ' + humanLabel(sv.key), api('POST', '/api/settings/' + encodeURIComponent(sv.key), { value: value })); } });
    });
    input.addEventListener('keydown', function (ev) { if (ev.key === 'Enter') { ev.preventDefault(); save.click(); } });
    var reset = el('button', { type: 'button', class: 'btn btn-sm btn-ghost', text: 'Reset', 'aria-label': 'Reset ' + sv.key + ' to default', disabled: !!sv.isDefault, title: 'Remove the override and fall back to the default' });
    reset.addEventListener('click', function () { run(reset, 'Reset ' + humanLabel(sv.key), api('DELETE', '/api/settings/' + encodeURIComponent(sv.key))); });
    row.appendChild(el('div', { class: 'setting-actions' }, [save, reset]));
    return row;
  }

  // The settings editor lives on the Auto tab: every settings.json key is
  // autoswitch.*, so a separate Settings tab would duplicate it.
  function renderSettings(st) {
    var body = $('auto-settings');
    clear(body);
    var list = st.settings;
    var avail = Array.isArray(list);
    $('auto-settings-empty').hidden = avail;
    if (!avail) { return; }
    var sections = {};
    var order = [];
    list.forEach(function (sv) {
      var sec = sv.key.indexOf('.') > 0 ? sv.key.split('.')[0] : 'general';
      if (!sections[sec]) { sections[sec] = []; order.push(sec); }
      sections[sec].push(sv);
    });
    order.forEach(function (sec) {
      if (order.length > 1) { body.appendChild(el('h3', { class: 'settings-section-title', text: sec })); }
      sections[sec].forEach(function (sv) { body.appendChild(settingRow(sv)); });
    });
  }

  document.addEventListener('click', function (ev) {
    var btn = ev.target.closest ? ev.target.closest('button[data-copy]') : null;
    if (!btn) { return; }
    var text = ($(btn.getAttribute('data-copy')) || {}).textContent || '';
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(text).then(function () { toast('Copied.', 'ok'); }, function () { toast('Copy failed — select the text and copy manually.'); });
    } else {
      toast('Copy is not available here — select the text and copy manually.');
    }
  });

  // ---- render root -----------------------------------------------------------

  // ---- guarded section rendering --------------------------------------------
  // A state event arrives every poll tick. Rebuilding a section's DOM on each
  // one destroyed whatever the user was typing into it (settings fields, the
  // transfer form) and reset open row menus and session groups. So
  // a section is repainted only when ITS slice of the state changed, never
  // while a control inside it has focus or a menu inside it is open (the
  // repaint is deferred until focus leaves / the menu closes), and open
  // <details> groups survive a rebuild.
  var renderSigs = {};
  var pendingRenders = {};

  // editingInside: a text-entry control (not a checkbox, radio or range — those
  // never lose typed state) or a BUTTON has focus inside the container, or a
  // row menu is open. Buttons count because a mouse-down on Save moves focus
  // from the input to the button; repainting at that instant destroys the
  // button under the pointer and the typed value with it.
  function editingInside(container) {
    if (!container) { return false; }
    var a = document.activeElement;
    if (a && a !== document.body && container.contains(a)) {
      var tag = a.tagName;
      // Only controls whose pending user input a rebuild would destroy count:
      // text entry anywhere, and selects/buttons inside a form or the settings
      // grid (a chosen strategy, a Save under the pointer). Static filters,
      // sort selects and action buttons (Start, Stop, Switch) keep focus after
      // a click and must not freeze their panel.
      var inForm = !!a.closest('form, .settings-grid, .modal');
      if (tag === 'TEXTAREA' || a.isContentEditable) { return true; }
      if ((tag === 'SELECT' || tag === 'BUTTON') && inForm) { return true; }
      if (tag === 'INPUT') {
        var t = (a.getAttribute('type') || 'text').toLowerCase();
        if (t !== 'checkbox' && t !== 'radio' && t !== 'range' && t !== 'button' && t !== 'submit') { return true; }
      }
    }
    return !!container.querySelector('details.menu[open]');
  }

  // sigOf: the change signature of a state slice. Fields that move every tick
  // without changing what is painted (usage freshness, server time, the event
  // ring the log consumes on its own path) are dropped, otherwise the guard
  // would repaint on every poll and defeat its purpose.
  var VOLATILE = { usageAgeSeconds: true, usageFetchedAt: true, serverTime: true, events: true, lastHookRun: true };
  function sigOf(data) {
    return JSON.stringify(data === undefined ? null : data, function (k, v) { return VOLATILE[k] ? undefined : v; });
  }

  function openDetailKeys(container) {
    var keys = {};
    if (!container) { return keys; }
    Array.prototype.forEach.call(container.querySelectorAll('details[open]'), function (d) {
      var k = d.getAttribute('data-key') || (d.querySelector('summary') || {}).textContent;
      if (k) { keys[k] = true; }
    });
    return keys;
  }

  function reopenDetails(container, keys) {
    if (!container) { return; }
    Array.prototype.forEach.call(container.querySelectorAll('details'), function (d) {
      if (d.classList.contains('menu')) { return; } // row menus never auto-reopen
      var k = d.getAttribute('data-key') || (d.querySelector('summary') || {}).textContent;
      if (k && keys[k]) { d.open = true; }
    });
  }

  function renderGuarded(key, containerId, data, fn) {
    var container = $(containerId);
    var sig = sigOf(data);
    if (renderSigs[key] === sig && !pendingRenders[key]) { return; }
    if (editingInside(container)) { pendingRenders[key] = { fn: fn, containerId: containerId, sig: sig }; return; }
    var open = openDetailKeys(container);
    renderSigs[key] = sig;
    delete pendingRenders[key];
    fn();
    reopenDetails(container, open);
  }

  function flushPendingRenders() {
    Object.keys(pendingRenders).forEach(function (key) {
      var p = pendingRenders[key];
      var container = $(p.containerId);
      if (editingInside(container)) { return; }
      var open = openDetailKeys(container);
      renderSigs[key] = p.sig;
      delete pendingRenders[key];
      p.fn();
      reopenDetails(container, open);
    });
  }
  document.addEventListener('focusout', function () { setTimeout(flushPendingRenders, 120); });
  document.addEventListener('click', function () { setTimeout(flushPendingRenders, 120); });
  document.addEventListener('toggle', function () { setTimeout(flushPendingRenders, 0); }, true);

  var pendingState = null;
  function render(st) {
    if (document.hidden) { pendingState = st; state = st; lastStateAt = Date.now(); return; } // paint once on return
    state = st;
    lastStateAt = Date.now();
    renderHeader(st);
    renderGuarded('dashboard', 'panel-dashboard',
      { accounts: st.accounts, active: st.activeNumber, strategies: st.strategies, settings: st.settings,
        auto: st.auto && { running: st.auto.running, threshold: st.auto.threshold, settings: st.auto.settings, quarantine: st.auto.quarantine } },
      function () { renderSummary(st); renderAccounts(st); });
    renderGuarded('auto', 'panel-auto',
      { auto: st.auto, accounts: st.accounts, active: st.activeNumber, settings: st.settings },
      function () { renderAuto(st); renderSettings(st); });
    renderGuarded('sessions', 'panel-sessions', st.sessions, function () { renderSessions(st.sessions); });
    renderGuarded('transfer', 'panel-transfer', { transfer: st.transfer, accounts: claudeRows(st).map(function (a) { return [a.number, a.email, a.alias]; }) }, function () { renderTransfer(st); });
    tickCountdowns();
  }

  // ---- actions (delegated) ---------------------------------------------------

  function findAccount(id) {
    var list = claudeRows(state);
    for (var i = 0; i < list.length; i++) { if (rowKey(list[i]) === String(id)) { return list[i]; } }
    return null;
  }

  function closeMenus() { document.querySelectorAll('details.menu[open]').forEach(function (d) { d.removeAttribute('open'); }); }

  var ACTIONS = {
    'switch-strategy': function (btn) {
      var models = parseModelNames(settingValue(state, 'autoswitch.model'));
      var body = { strategy: strategy };
      if (models.length) { body.models = models; }
      return run(btn, 'Switch (' + strategy + ')', api('POST', '/api/switch', body));
    },
    'add-current': function (btn) {
      return confirmModal('Add current login', 'Snapshot the Claude Code login that is active right now into a new slot?', 'Add').then(function (ok) {
        if (!ok) { return; }
        return run(btn, 'Add current login', api('POST', '/api/accounts/add', {}));
      });
    },
    'add-token': function (btn) {
      return openModal({
        title: 'Add account from token',
        message: 'Register a setup-token (sk-ant-oat01-…) or an API key (sk-ant-api…). The token is sent once and never shown again.',
        fields: [
          { name: 'token', label: 'Token', type: 'password', required: true, placeholder: 'sk-ant-…' },
          { name: 'email', label: 'Email', type: 'email', placeholder: 'me@example.com', hint: 'Optional; identifies the account in lists.' },
          { name: 'slot', label: 'Slot', type: 'text', placeholder: 'next free', hint: 'Optional slot number.' },
          { name: 'alias', label: 'Alias', type: 'text', placeholder: 'work', hint: 'Optional short name.' }
        ],
        okLabel: 'Add'
      }).then(function (v) {
        if (!v) { return; }
        var body = { token: v.token };
        if (v.email) { body.email = v.email; }
        if (v.slot) { body.slot = v.slot; }
        if (v.alias) { body.alias = v.alias; }
        return run(btn, 'Add token', api('POST', '/api/accounts/add-token', body));
      });
    },
    'force-switch': function (btn) {
      var id = btn.getAttribute('data-id');
      return confirmModal('Force switch', 'Switch to ' + btn.getAttribute('data-name') + ' WITHOUT backing up the current login first? Unsaved changes to the active login are lost.', 'Force switch').then(function (ok) {
        if (!ok) { return; }
        return run(btn, 'Force switch', api('POST', '/api/switch/' + encodeURIComponent(id) + '?force=1'));
      });
    },
    'alias': function (btn) {
      var id = btn.getAttribute('data-id');
      return openModal({
        title: 'Alias for ' + btn.getAttribute('data-name'),
        message: 'A short name you can use anywhere an account is referenced. Leave empty to remove it.',
        fields: [{ name: 'alias', label: 'Alias', type: 'text', value: btn.getAttribute('data-alias') || '', placeholder: 'work' }],
        okLabel: 'Save'
      }).then(function (v) {
        if (!v) { return; }
        return run(btn, v.alias ? 'Set alias' : 'Remove alias', api('POST', '/api/accounts/' + encodeURIComponent(id) + '/alias', { alias: v.alias }));
      });
    },
    'move': function (btn) {
      var id = btn.getAttribute('data-id');
      return openModal({
        title: 'Move ' + btn.getAttribute('data-name'),
        message: 'Assign the account to a slot number. If the slot is taken, the two accounts swap.',
        fields: [{ name: 'slot', label: 'Target slot', type: 'text', required: true, placeholder: '3' }],
        okLabel: 'Move'
      }).then(function (v) {
        if (!v) { return; }
        return run(btn, 'Move', api('POST', '/api/accounts/' + encodeURIComponent(id) + '/move', { slot: v.slot }));
      });
    },
    'swap': function (btn) {
      var id = btn.getAttribute('data-id');
      var options = claudeRows(state).filter(function (a) { return rowKey(a) !== String(id); }).map(function (a) { return { value: rowKey(a), label: accountLabel(a) }; });
      if (!options.length) { toast('No other account to swap with.'); return Promise.resolve(); }
      return openModal({
        title: 'Swap ' + btn.getAttribute('data-name'),
        message: 'Exchange slot numbers with another account.',
        fields: [{ name: 'other', label: 'Swap with', type: 'select', options: options, value: options[0].value }],
        okLabel: 'Swap'
      }).then(function (v) {
        if (!v) { return; }
        return run(btn, 'Swap', api('POST', '/api/accounts/swap', { a: String(id), b: v.other }));
      });
    },
    'remove': function (btn) {
      var id = btn.getAttribute('data-id');
      var a = findAccount(id);
      return confirmModal('Remove account', 'Remove ' + btn.getAttribute('data-name') + ' and its stored credentials from ' + NAME + '? ' + (a && a.isActive ? 'This is the ACTIVE account. ' : '') + 'This cannot be undone.', 'Remove').then(function (ok) {
        if (!ok) { return; }
        return run(btn, 'Remove', api('POST', '/api/accounts/' + encodeURIComponent(id) + '/remove'));
      });
    },
    'auto-start': function (btn) {
      var dry = btn.getAttribute('data-dry') === '1';
      return run(btn, dry ? 'Start dry-run' : 'Start', api('POST', '/api/auto/start', { dryRun: dry }));
    },
    'export': function (btn) {
      var body = { path: $('export-path').value.trim(), account: $('export-account').value, full: $('export-full').checked, overwrite: $('export-overwrite').checked };
      if (!body.path) { toast('Choose a file to export to.'); $('export-path').focus(); return Promise.resolve(); }
      return confirmModal('Export accounts', 'Write ' + (body.account ? 'account #' + body.account : 'every account') + ' to ' + body.path + '? The file holds live credentials in plain text: keep it like a password and delete it once it is imported.', 'Export').then(function (ok) {
        if (!ok) { return; }
        return transferRun(btn, 'Export', api('POST', '/api/transfer/export', body));
      });
    },
    'import': function (btn) {
      var body = { path: $('import-path').value.trim(), force: $('import-force').checked };
      if (!body.path) { toast('Choose a file to import.'); $('import-path').focus(); return Promise.resolve(); }
      var go = body.force ? confirmModal('Import and overwrite', 'Overwrite the local slots that match accounts in ' + body.path + '?', 'Import') : Promise.resolve(true);
      return go.then(function (ok) {
        if (!ok) { return; }
        return transferRun(btn, 'Import', api('POST', '/api/transfer/import', body));
      });
    }
  };

  // ---- transfer ----------------------------------------------------------------

  // transferRun is run() plus the messages the CLI would have printed, shown
  // under the form (skipped slots, overwritten slots, the summary).
  function transferRun(btn, label, promise) {
    var out = $('transfer-out');
    clear(out);
    return run(btn, label, promise.then(function (data) {
      var msgs = (data && data.result && data.result.messages) || [];
      msgs.forEach(function (m) { out.appendChild(el('li', { text: m })); });
      out.hidden = msgs.length === 0;
      return data;
    }));
  }

  function renderTransfer(st) {
    var on = !!st.transfer;
    $('transfer-cards').hidden = !on;
    $('transfer-unavailable').hidden = on;
    var sel = $('export-account');
    if (document.activeElement === sel) { return; }
    var cur = sel.value;
    clear(sel);
    sel.appendChild(el('option', { value: '', text: 'Every account' }));
    claudeRows(st).forEach(function (a) { sel.appendChild(el('option', { value: String(a.number), selected: String(a.number) === cur, text: accountLabel(a) })); });
  }

  document.addEventListener('click', function (ev) {
    var btn = ev.target.closest ? ev.target.closest('button[data-post], button[data-action]') : null;
    if (!btn || btn.disabled) { return; }
    var action = btn.getAttribute('data-action');
    if (action && ACTIONS[action]) {
      closeMenus();
      ACTIONS[action](btn);
      return;
    }
    var url = btn.getAttribute('data-post');
    if (!url || inflight[url]) { return; }
    closeMenus();
    var label = btn.getAttribute('data-label') || 'Action';
    var confirmMsg = btn.getAttribute('data-confirm');
    var go = confirmMsg ? confirmModal(label, confirmMsg, label) : Promise.resolve(true);
    go.then(function (ok) {
      if (!ok) { return; }
      inflight[url] = true;
      return run(btn, label, api('POST', url)).then(function () { delete inflight[url]; });
    });
  });

  // Count model limits: on -> autoswitch.model = "all" (every per-model weekly
  // window counts towards headroom and at-limit), off -> unset (5h + 7d only).
  // Naming specific models is still possible in the settings field below. The
  // setting is saved AND applied to a running engine, so Next best changes at
  // once instead of after a restart.
  $('model-limits-toggle').addEventListener('change', function (ev) {
    var on = !!ev.target.checked;
    var saved = on ? api('POST', '/api/settings/' + encodeURIComponent('autoswitch.model'), { value: 'all' })
                   : api('DELETE', '/api/settings/' + encodeURIComponent('autoswitch.model'));
    var req = saved.then(function (r) {
      if (!(state && state.auto && state.auto.running)) { return r; }
      return api('POST', '/api/auto/model', { model: on ? 'all' : '' }).then(function () { return r; });
    });
    ev.target.blur(); // let the next render set the checkbox from the saved setting
    run(ev.target, on ? 'Count model limits' : 'Ignore model limits', req).then(function () { return loadOnce(); }).catch(function () {});
  });

  $('token-status-toggle').addEventListener('change', function (ev) {
    showTokenStatus = !!ev.target.checked;
    // The stream carries token status only for a subscriber that asks for it,
    // so resubscribe with the new choice instead of copying stale values.
    loadOnce().then(function () { resubscribe(); }).catch(function (err) { toast(err.message); });
  });

  // ---- data flow -------------------------------------------------------------

  function loadOnce() {
    return api('GET', '/api/state' + (showTokenStatus ? '?tokenStatus=1' : '')).then(render);
  }

  var es = null;
  var sessionDead = false;
  function resubscribe() { if (es) { es.close(); es = null; } if (!sessionDead) { subscribe(); } }

  // deadSession: the server is back but no longer knows this page's tokens (it
  // was restarted, and every launch mints new secrets). Retrying would fail
  // forever, so stop and say what to do.
  function deadSession() {
    sessionDead = true;
    if (es) { es.close(); es = null; }
    setConn('off');
    $('conn-text').textContent = 'session ended';
    toast('This dashboard session has ended. Run ' + NAME + ' web again and open the URL it prints.');
  }

  function subscribe() {
    if (!window.EventSource) { setConn('off'); setInterval(function () { loadOnce().catch(function () {}); }, 5000); return; }
    setConn('wait');
    es = new EventSource('/api/events?csrf=' + encodeURIComponent(CSRF) + (showTokenStatus ? '&tokenStatus=1' : ''));
    es.addEventListener('open', function () { setConn('on'); });
    es.addEventListener('state', function (ev) {
      try {
        render(JSON.parse(ev.data));
        setConn('on');
      } catch (e) { toast('Bad state event: ' + e.message); }
    });
    es.addEventListener('auto', function (ev) {
      try { appendAutoEvent(JSON.parse(ev.data), false); renderAutoLog(); } catch (e) { /* ignore malformed */ }
    });
    var mine = es;
    es.addEventListener('error', function () {
      if (mine !== es) { return; }
      if (mine.readyState !== 2) { setConn('wait'); return; }
      // CLOSED: the stream was refused (a non-200 answer) or the server went
      // away. Ask once with fetch, which sees the status EventSource hides:
      // 401/403 means the tokens are stale, anything else means try again.
      setConn('off');
      mine.close();
      setTimeout(function () {
        if (mine !== es) { return; }
        fetch('/api/state', { credentials: 'same-origin', headers: { 'X-CSRF-Token': CSRF } }).then(function (res) {
          if (res.status === 401 || res.status === 403) { deadSession(); } else { subscribe(); }
        }, function () { subscribe(); });
      }, 3000);
    });
  }

  if (!window.EventSource) { loadOnce().catch(function (err) { toast(err.message); setConn('off'); }); }
  subscribe();
  setInterval(function () { if (!document.hidden) { tickCountdowns(); } }, 1000);
  document.addEventListener('visibilitychange', function () {
    if (!document.hidden) { tickCountdowns(); if (pendingState) { var ps = pendingState; pendingState = null; render(ps); } }
  });
})();
