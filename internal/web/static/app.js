// app.js — tycswap dashboard client (DESIGN A26). Vanilla JS, no build step:
// take the CSRF token from the launch redirect's #csrf= fragment (kept in
// sessionStorage, so a reload keeps it and a new tab needs a fresh launch
// URL), fetch /api/state once, then follow /api/events (SSE) with the
// browser's built-in reconnect. Countdowns are recomputed client-side every second from
// the server's resets_at / expiresAt / startedAt values. The Auto tab ranks
// "Next best" candidates client-side by availability, reset time and all
// counted usage windows, and colours engine events like
// tui eventColor. Components: tile(), meter(), chip() — one implementation
// each, reused on every tab. DESIGN A27 adds the Updates card and header
// indicator, the Settings tab, the foldable cards, the add-current-login
// callout and the auth-overrides notice; the Guide tab is static markup.
(function () {
  'use strict';

  // CSRF: the second factor. The launch redirect lands on /#csrf=<token>;
  // the fragment never reaches a server, so the page is the only thing that
  // sees it. It is moved into sessionStorage (per tab, gone when the tab
  // closes; a reload keeps it) and the fragment is dropped from the URL and
  // the history entry at once. A tab without a token cannot have come from
  // the launch URL and is told to open it.
  var CSRF = (function () {
    var key = 'csrf';
    var m = /^#csrf=([0-9a-f]+)$/.exec(window.location.hash || '');
    if (m) {
      try { sessionStorage.setItem(key, m[1]); } catch (e) { /* storage disabled: the token lives for this load only */ }
      try { history.replaceState(null, '', window.location.pathname + window.location.search); } catch (e) { /* ignore */ }
      return m[1];
    }
    try { return sessionStorage.getItem(key) || ''; } catch (e) { return ''; }
  })();
  // The command name for the hints this page prints (brand.Name, templated).
  var NAME = (document.querySelector('meta[name="app-name"]') || {}).content || 'tycswap';
  var $ = function (id) { return document.getElementById(id); };
  var state = null;
  var minimumStateSequence = 0;
  var autoModelPicker = null;
  var modelSettingRow = null;
  var lastStateAt = 0;
  var inflight = {};
  var showTokenStatus = false;
  var strategy = 'best';
  var accountGroup = '';
  var accountOrder = { key: 'best', direction: 1 };
  try { accountGroup = sessionStorage.getItem('accountGroup') || ''; } catch (e) {}
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
  // Rows carry provider and key: the Claude rows come first, then, on a
  // machine with Codex accounts, the Codex rows, whose slot numbers overlap
  // the Claude ones (DESIGN A47). What is about Claude Code (the header
  // strip, the summary, the Next best ranking) reads claudeRows; the account
  // table lists both groups.
  function isClaude(a) { return (a.provider || 'claude') === 'claude'; }
  function claudeRows(st) { return ((st && st.accounts) || []).filter(isClaude); }
  function codexRows(st) { return ((st && st.accounts) || []).filter(function (a) { return !isClaude(a); }); }
  // rowKey is the account's API address: every account route takes the row
  // key ("claude:2"), never a bare slot, because slot numbers are per provider.
  function rowKey(a) { return a.key || ((a.provider || 'claude') + ':' + a.number); }
  function keyPath(a) { return encodeURIComponent(rowKey(a)); }
  function accountLabel(a) {
    var parts = [a.alias, a.email].filter(Boolean);
    return '#' + a.number + (parts.length ? ' ' + parts.join(' \u00b7 ') : '');
  }
  // hostOf is the host (and port) of an API-key account's base URL, which is
  // what a row shows; the whole URL is in its tooltip (DESIGN A46).
  function hostOf(u) {
    try { return new URL(u).host || u; } catch (e) { return u; }
  }

  // toast shows a message: kind 'ok' (done), 'info' (something to act on
  // that is not a failure, in the plain accent style) or anything else (an
  // error).
  function toast(msg, kind) {
    var box = $('toasts');
    var info = kind === 'ok' || kind === 'info';
    var t = el('div', { class: 'toast' + (kind === 'ok' ? ' toast-ok' : kind === 'info' ? '' : ' toast-err'), role: info ? 'status' : 'alert', text: msg });
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
        if (!res.ok) {
          var e = new Error((data && data.error) || ('HTTP ' + res.status));
          if (data && data.output) { e.output = data.output; } // what an update's commands printed
          throw e;
        }
        if (data && data.stateSequence) { minimumStateSequence = Math.max(minimumStateSequence, data.stateSequence); }
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
  function selectTab(name, focus, hash) {
    tabs.forEach(function (t) {
      var on = t.getAttribute('data-tab') === name;
      t.setAttribute('aria-selected', on ? 'true' : 'false');
      t.tabIndex = on ? 0 : -1;
      var panel = $('panel-' + t.getAttribute('data-tab'));
      if (panel) { panel.hidden = !on; }
      if (on && focus) { t.focus(); }
    });
    var targetHash = hash || '#' + name;
    if (window.location.hash !== targetHash) {
      try { history.replaceState(null, '', targetHash); } catch (e) { /* ignore */ }
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
    var section = $(h);
    if (section && section.classList.contains('guide-section')) {
      // A contents link names a section inside Guide, not another tab. Keep
      // its fragment for reload/Back, then scroll after revealing the panel.
      selectTab('guide', false, '#' + h);
      section.scrollIntoView({ block: 'start' });
      return;
    }
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
      if (opts.preview) { fields.appendChild(el('pre', { class: 'handover-preview', text: opts.preview, tabindex: '0' })); }
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
  // AutoView.Settings map is keyed "autoswitch.<key>"), falling back to the
  // saved settings.json value when not running.
  function engineSetting(st, key) {
    var a = st.auto || {};
    var s = (a.running && a.settings) || {};
    if (s[key] !== undefined && s[key] !== null && s[key] !== '') { return s[key]; }
    return settingValue(st, key);
  }

  function groupModels(st, id) {
    var group = (st.groups || []).filter(function (g) { return g.id === id; })[0];
    if (group && group.settings) { return parseModelNames(group.settings['autoswitch.model']); }
    if (id === 'opus') { return []; }
    return modelWindowNames(st).filter(function (name) { return /fable/i.test(name); });
  }

  function groupState(st, id) {
    if (!id) {
      var owners = {};
      (st.groups || []).forEach(function (g) { if (g.activeNumber) { owners[String(g.activeNumber)] = g.label || g.id; } });
      return Object.assign({}, st, { accounts: (st.accounts || []).map(function (a) {
        var owner = isClaude(a) && owners[String(a.number)];
        return owner ? Object.assign({}, a, { groupBlocker: 'in use by ' + owner, rotationEligible: false }) : a;
      }) });
    }
    var group = (st.groups || []).filter(function (g) { return g.id === id; })[0] || {};
    var models = groupModels(st, id);
    if (id === 'fable' && !models.length) { models = ['Fable']; }
    models = models.map(function (name) { return name.toLowerCase(); });
    var view = Object.assign({}, st, { activeNumber: group.activeNumber || null, groupId: id });
    view.settings = (st.settings || []).map(function (s) {
      if (s.key === 'autoswitch.model') { return Object.assign({}, s, { value: models.join(',') }); }
      return group.settings && Object.prototype.hasOwnProperty.call(group.settings, s.key) ? Object.assign({}, s, { value: group.settings[s.key] }) : s;
    });
    if (!view.settings.some(function (s) { return s.key === 'autoswitch.model'; })) {
      view.settings.push({ key: 'autoswitch.model', value: models.join(',') });
    }
    view.auto = Object.assign({}, st.auto || {}, group.auto || {});
    view.auto.settings = Object.assign({}, view.auto.settings || {}, group.settings || {}, { 'autoswitch.model': models.join(',') });
    view.accounts = (st.accounts || []).map(function (a) {
      if (!isClaude(a)) { return a; }
      var wins = relevantWindows(a.usage, models.map(function (m) { return m.toLowerCase(); }));
      var blocked = group.accountBlockers && group.accountBlockers[String(a.number)];
      var visibleUsage = a.usage && Object.assign({}, a.usage, { scoped: (a.usage.scoped || []).filter(function (w) { return models.indexOf(String(w.name || '').toLowerCase()) >= 0; }) });
      return Object.assign({}, a, { isActive: String(a.number) === String(group.activeNumber || ''),
        usage: visibleUsage,
        atLimit: wins.some(function (w) { return w.pct >= 100; }),
        limitingWindows: wins.filter(function (w) { return w.pct >= 100; }).map(function (w) { return w.label; }),
        groupBlocker: blocked || '', rotationEligible: a.rotationEligible && !blocked });
    });
    return view;
  }

  function displayAccounts(st, ordering) {
    var list = claudeRows(st).slice();
    var ranked = rankCandidates(st).ranked;
    var ranks = {};
    ranked.forEach(function (r, i) { ranks[r.number] = i; });
    var models = parseModelNames(engineSetting(st, 'autoswitch.model'));
    function values(a) {
      var cls = classPcts(relevantWindows(a.usage, models));
      var model = bindingPct(((a.usage || {}).scoped || []).map(function (w) { return { pct: pctNum(w.pct) }; }).filter(function (w) { return w.pct !== null; }));
      return { fiveHour: cls.fiveHour, sevenDay: cls.sevenDay, model: ordering.key === 'model' ? model : cls.model };
    }
    return list.sort(function (a, b) {
      if (ordering.key === 'best') {
        if (a.isActive !== b.isActive) { return a.isActive ? -1 : 1; }
        var ra = ranks[String(a.number)], rb = ranks[String(b.number)];
        return (ra === undefined ? 100000 : ra) - (rb === undefined ? 100000 : rb) || Number(a.number) - Number(b.number);
      }
      if (ordering.key === 'number') { return (Number(a.number) - Number(b.number)) * ordering.direction; }
      var av = values(a), bv = values(b);
      var keys = [ordering.key].concat(['sevenDay', 'model', 'fiveHour'].filter(function (k) { return k !== ordering.key; }));
      for (var i = 0; i < keys.length; i++) {
        var x = av[keys[i]], y = bv[keys[i]];
        if (x === null || y === null) { if (x !== y) { return x === null ? 1 : -1; } }
        else if (x !== y) { return (x - y) * ordering.direction; }
      }
      return Number(a.number) - Number(b.number);
    });
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

  // classPcts mirrors oauth.ClassPcts: the worst utilization in each window
  // class, kept apart because each has a bar of its own (DESIGN A34): the 5h
  // window bursts, the week creeps, a per-model week is one model's budget.
  function classPcts(wins) {
    var out = { fiveHour: null, sevenDay: null, model: null };
    wins.forEach(function (w) {
      var k = w.label === '5h' ? 'fiveHour' : (w.label === '7d' ? 'sevenDay' : 'model');
      if (out[k] === null || w.pct > out[k]) { out[k] = w.pct; }
    });
    return out;
  }

  // weeklyPct is the worse of the two budget figures, the week and a counted
  // model window, which is what ranking compares.
  function weeklyPct(cls) {
    if (cls.sevenDay === null) { return cls.model; }
    if (cls.model === null) { return cls.sevenDay; }
    return Math.max(cls.sevenDay, cls.model);
  }

  // overAnyBar: has any window reached the bar that governs it?
  function overAnyBar(cls, bars) {
    return (cls.fiveHour !== null && cls.fiveHour >= bars.fiveHour) ||
           (cls.sevenDay !== null && cls.sevenDay >= bars.sevenDay) ||
           (cls.model !== null && cls.model >= bars.model);
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
    return entry.reason || '';
  }

  // quarantineSince: the entry's "at" as Unix seconds. The engine writes an
  // RFC3339 stamp; a number is taken as seconds already.
  function quarantineSince(entry) {
    if (!entry || typeof entry !== 'object') { return null; }
    var v = entry.at;
    if (typeof v === 'number') { return v; }
    if (typeof v === 'string') { var t = Date.parse(v); return isNaN(t) ? null : t / 1000; }
    return null;
  }

  var SENTINEL_STATUSES = { token_expired: 'token expired', api_key: 'api key', keychain_unavailable: 'keychain unavailable', relogin_required: 're-login needed', no_credentials: 'no credentials' };

  // rankCandidates mirrors tui/autoview.go candidatesText: each window against
  // its own bar (DESIGN A34). The panel compares 7d, model and 5h
  // hierarchically rather than collapsing the windows into one percentage.
  function requiredModelWindows(st, models) {
    var names = models.filter(function (name) { return name !== 'all'; });
    if (models.indexOf('all') >= 0) {
      claudeRows(st).forEach(function (a) {
        ((a.usage || {}).scoped || []).forEach(function (w) {
          var name = String(w.name || '').toLowerCase();
          if (name && pctNum(w.pct) !== null && names.indexOf(name) < 0) { names.push(name); }
        });
      });
      if (!names.length) { names.push('all'); }
    }
    return names;
  }
  function missingModelWindows(usage, required) {
    var reported = ((usage || {}).scoped || []).filter(function (w) { return pctNum(w.pct) !== null; }).map(function (w) { return String(w.name || '').toLowerCase(); });
    return required.filter(function (name) { return reported.indexOf(name) < 0; });
  }

  function rankCandidates(st) {
    var now = Date.now() / 1000;
    var auto = st.auto || {};
    var models = parseModelNames(engineSetting(st, 'autoswitch.model'));
    var required = requiredModelWindows(st, models);
    var strat = engineSetting(st, 'autoswitch.strategy') || 'soonest-reset';
    // One bar per window. The 7d one follows the running engine when the
    // slider has moved it; the other two are settings only.
    var sevenDay = pctNum(settingValue(st, 'autoswitch.sevenDayThreshold')) || 97;
    if (auto.running && typeof auto.threshold === 'number') { sevenDay = auto.threshold; }
    var bars = {
      fiveHour: pctNum(engineSetting(st, 'autoswitch.fiveHourThreshold')) || 85,
      sevenDay: sevenDay,
      model: pctNum(engineSetting(st, 'autoswitch.modelThreshold')) || 95
    };
    var quarantine = quarantineMap(auto);
    var ranked = [];
    claudeRows(st).forEach(function (a) {
      if (a.isActive || !a.rotationEligible) { return; }
      var num = String(a.number);
      var r = { account: a, number: num, bestKey: 0, tier: 0, pct: 0, renewal: null, label: '', windows: [], order: [], readyAt: null, soonReset: false };
      if (Object.prototype.hasOwnProperty.call(quarantine, num)) {
        var reason = quarantineReason(quarantine[num]);
        r.label = reason ? 'quarantined (' + reason + ')' : 'quarantined';
        r.bestKey = 997; r.tier = 4;
      } else if (a.usageStatus && a.usageStatus !== 'ok' && SENTINEL_STATUSES[a.usageStatus]) {
        r.label = SENTINEL_STATUSES[a.usageStatus]; r.bestKey = 998; r.tier = 5;
      } else if (a.usage && missingModelWindows(a.usage, required).length) {
        r.label = 'model usage missing: ' + missingModelWindows(a.usage, required).join(', '); r.bestKey = 998; r.tier = 5;
      } else {
        var wins = relevantWindows(a.usage, models);
        var pct = bindingPct(wins);
        if (pct === null) {
          r.label = 'usage unknown'; r.bestKey = 999; r.tier = 6;
        } else {
          // Judge each window against its own bar. Weekly headroom is
          // displayed separately from the hierarchical sorting keys; missing
          // weekly data falls back to the binding figure.
          var cls = classPcts(wins);
          var weekly = weeklyPct(cls);
          var key = weekly === null ? pct : weekly;
          r.order = [cls.sevenDay === null ? key : cls.sevenDay,
                     cls.model === null ? 0 : cls.model,
                     cls.fiveHour === null ? 0 : cls.fiveHour];
          r.windows = wins; r.bestKey = key; r.pct = key; r.renewal = renewalTS(wins);
          if (pct >= 100) {
            r.tier = 3;
            r.readyAt = limitingReset(wins, now);
            r.soonReset = r.readyAt !== null && r.readyAt - now < 5 * 3600;
          }
          else if (overAnyBar(cls, bars)) { r.tier = 2; }
          else if (r.renewal !== null) { r.tier = 0; }
          else { r.tier = 1; }
        }
      }
      ranked.push(r);
    });
    var less = strat === 'soonest-reset' ? lessSoonest : lessBest;
    ranked.sort(function (a, b) { return less(a, b) ? -1 : less(b, a) ? 1 : 0; });
    return { ranked: ranked, models: models, strategy: strat, bars: bars };
  }

  function numLess(a, b) { var x = parseInt(a, 10), y = parseInt(b, 10); if (!isNaN(x) && !isNaN(y)) { return x < y; } return a < b; }

  // An exhausted account is only available soon when EVERY counted full
  // window resets soon. Missing or elapsed reset stamps cannot prove that.
  function limitingReset(wins, now) {
    var full = wins.filter(function (w) { return w.pct >= 100; });
    var latest = null;
    for (var i = 0; i < full.length; i++) {
      var at = Date.parse(full[i].resetsAt) / 1000;
      if (!isFinite(at) || at <= now) { return null; }
      latest = latest === null ? at : Math.max(latest, at);
    }
    return latest;
  }

  function candidateGroup(r, best) {
    if (best && r.tier < 2) { return 0; }
    if (r.tier === 3) { return r.soonReset ? 3 : 4; }
    return r.tier >= 4 ? r.tier + 1 : r.tier;
  }

  // Lower usage wins at the first differing window; account number only
  // breaks a complete tie. An unreported model or burst limit adds no limit.
  function compareWindows(a, b) {
    for (var i = 0; i < a.order.length; i++) {
      if (a.order[i] !== b.order[i]) { return a.order[i] - b.order[i]; }
    }
    return 0;
  }

  function lessBest(a, b) {
    var groupA = candidateGroup(a, true), groupB = candidateGroup(b, true);
    if (groupA !== groupB) { return groupA < groupB; }
    if (a.soonReset && b.soonReset && a.readyAt !== b.readyAt) { return a.readyAt < b.readyAt; }
    // Keep unavailable rows at the end, in their existing reason order.
    if (a.tier >= 4 || b.tier >= 4) {
      if (a.bestKey !== b.bestKey) { return a.bestKey < b.bestKey; }
    } else {
      var cmp = compareWindows(a, b);
      if (cmp) { return cmp < 0; }
    }
    return numLess(a.number, b.number);
  }

  function lessSoonest(a, b) {
    var groupA = candidateGroup(a, false), groupB = candidateGroup(b, false);
    if (groupA !== groupB) { return groupA < groupB; }
    if (a.soonReset && b.soonReset && a.readyAt !== b.readyAt) { return a.readyAt < b.readyAt; }
    if (a.tier === 0 || a.tier === 3) {
      if ((a.renewal !== null) !== (b.renewal !== null)) { return a.renewal !== null; }
      if (a.renewal !== null && b.renewal !== null && a.renewal !== b.renewal) { return a.renewal < b.renewal; }
    }
    if (a.tier < 4) {
      var cmp = compareWindows(a, b);
      if (cmp) { return cmp < 0; }
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
      var scoped = (a && a.usage && a.usage.scoped) || [];
      if (Array.isArray(scoped)) { scoped.forEach(function (w) { if (w && w.name) { names[w.name] = true; } }); }
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
    return el('span', { class: 'chip chip-warn', title: 'Choose and save windows under "Count model limits" on the Auto tab, or autoswitch.model on the Settings tab', text: names.join(', ') + ' limits ignored' });
  }

  // ---- header + summary ------------------------------------------------------

  function renderHeader(st) {
    var v = $('version');
    v.textContent = st.version ? 'v' + st.version : '';
    v.hidden = !st.version;
    var accts = st.accounts || [];
    $('badge-dashboard').textContent = accts.length ? String(accts.length) : '';
    var sess = (st.sessions && st.sessions.claude) || [];
    var badgeS = $('badge-sessions');
    badgeS.textContent = sess.length ? String(sess.length) : '';
    var busy = sess.filter(function (c) { return c.status === 'busy'; }).length;
    badgeS.className = 'tab-badge' + (busy ? ' hot' : '');
    badgeS.title = busy ? busy + ' busy' : '';
    var badgeA = $('badge-auto');
    if (st.auto && st.auto.running) { badgeA.textContent = st.auto.dryRun ? 'dry' : '●'; badgeA.className = 'tab-badge live'; badgeA.title = st.auto.dryRun ? 'engine running (dry-run)' : 'engine running'; }
    else { badgeA.textContent = ''; badgeA.className = 'tab-badge'; }
    // Settings: how many keys are set away from their default.
    var custom = (Array.isArray(st.settings) ? st.settings : []).filter(function (sv) { return !sv.isDefault; }).length;
    var badgeSt = $('badge-settings');
    badgeSt.textContent = custom ? String(custom) : '';
    badgeSt.title = custom ? custom + ' setting' + (custom === 1 ? '' : 's') + ' changed from the default' : '';
    renderActiveStrip(st);
    renderUpdatesBadge(st);
  }

  // ---- onboarding (DESIGN A27) ------------------------------------------------
  // What keeps Claude Code from using the stored login: an auth override in
  // the server's environment or in Claude Code's settings.json. Names only;
  // the server never sends a value.
  function renderOnboarding(st) {
    var ov = st.authOverrides || { env: [], settings: [] };
    var envKeys = ov.env || [], setKeys = ov.settings || [];
    var any = envKeys.length > 0 || setKeys.length > 0;
    $('auth-overrides').hidden = !any;
    $('auth-overrides-env').hidden = !envKeys.length;
    $('auth-overrides-env-keys').textContent = envKeys.join(', ');
    $('auth-overrides-settings').hidden = !setKeys.length;
    $('auth-overrides-settings-keys').textContent = setKeys.join(', ');
    $('auth-overrides-path').textContent = ov.settingsPath || 'settings.json';
    // An empty wrapper would still take a grid gap at the top of the panel.
    $('onboard').hidden = !any;
  }

  // ---- updates (DESIGN A27) ---------------------------------------------------
  // What can be updated (this program, Claude Code) as the server last saw
  // it: a prominent card while anything waits, a quiet "up to date" line
  // otherwise, and the header indicator on every tab. An apply runs in the
  // server and can take minutes. The target in flight is kept here, so the
  // repaints the state stream causes meanwhile keep its button busy, and the
  // outcome stays on the page until it is dismissed.

  var updApplying = null; // 'app' | 'claude-code' while one runs
  var updResult = null;   // { ok, pending, message, output } of the last apply

  function bareVersion(v) { return String(v || '').replace(/^v/, ''); }

  // agoSpan renders "3m ago" for an RFC3339 stamp and keeps it ticking.
  function agoSpan(iso) {
    var t = Date.parse(iso);
    if (isNaN(t)) { return el('span', { text: '—' }); }
    return el('span', { 'data-started': String(t), text: fmtAgo(Date.now() - t) });
  }

  // updateCount: one per thing the user would update.
  function updateCount(u) {
    if (!u || !u.available) { return 0; }
    var n = (u.app && u.app.available ? 1 : 0) + (u.claudeCode && u.claudeCode.available ? 1 : 0);
    return Math.max(n, 1); // "available" with nothing itemised is still one
  }

  // updateLines: one short line per update, for the header indicator's tooltip.
  function updateLines(u) {
    var out = [];
    if (u.app && u.app.available) { out.push(NAME + ' ' + bareVersion(u.app.current) + ' → ' + bareVersion(u.app.latest)); }
    if (u.claudeCode && u.claudeCode.available) { out.push('Claude Code ' + (u.claudeCode.installed || '?') + ' → ' + (u.claudeCode.latest || 'latest')); }
    return out;
  }

  // updateProblems: what the last check could not read (errors), and what is
  // said rather than offered (notes: Claude Code not installed, a build that
  // upgrades itself another way). While there is an error the page never
  // says everything is up to date.
  function updateProblems(u) {
    var errors = [], notes = [];
    if (u.app && u.app.error) { errors.push(['Could not check for a new ' + NAME + ': ' + u.app.error]); }
    if (u.app && u.app.installed) { notes.push([NAME + ' ' + bareVersion(u.app.latest) + ' is installed; this server still runs ' + bareVersion(u.app.current) + ' until you start ', el('code', { text: NAME + ' web' }), ' again.']); }
    else if (u.app && u.app.available && u.app.hint) { notes.push([NAME + ' ' + bareVersion(u.app.latest) + ' is out, and this build upgrades from a terminal: ', el('code', { text: u.app.hint }), '.']); }
    var cc = u.claudeCode;
    if (cc && cc.error) {
      errors.push(['Could not check for a newer Claude Code: ' + cc.error]);
    } else if (cc && cc.state === 'unknown' && cc.detail) {
      errors.push([cc.detail]);
    } else if (cc && cc.state === 'missing') {
      var note = [cc.detail || 'Claude Code is not installed on this machine.'];
      if (cc.command) { note.push(' Install it with ', el('code', { text: cc.command }), '.'); }
      notes.push(note);
    }
    return { errors: errors, notes: notes };
  }

  function renderUpdatesBadge(st) {
    var u = st.updates;
    var n = updateCount(u);
    var b = $('hdr-updates');
    b.hidden = !n;
    if (b.hidden) { b.title = ''; return; }
    var text = n === 1 ? 'Update available' : n + ' updates available';
    $('hdr-updates-text').textContent = text;
    $('hdr-updates-count').textContent = n > 9 ? '9+' : String(n);
    b.setAttribute('aria-label', text + '. Show the updates.');
    b.title = updateLines(u).join('\n');
  }

  // applyButton is one update's button; the target in flight stays busy
  // across repaints.
  function applyButton(target, label, doing) {
    var busy = updApplying === target;
    var b = el('button', { type: 'button', class: 'btn btn-primary', 'data-action': 'updates-apply', 'data-target': target, disabled: !!updApplying, text: busy ? (doing || 'Working…') : label });
    if (busy) { b.setAttribute('aria-busy', 'true'); }
    return b;
  }

  // updItem is one row of the card: name, versions, notes, the button.
  function updItem(name, versions, notes, button, method) {
    var body = el('div', { class: 'upd-body' }, [
      el('div', { class: 'upd-line' }, [el('span', { class: 'upd-name', text: name }), el('span', { class: 'upd-ver', text: versions }), method ? chip(method, 'outline') : null]),
      el('div', { class: 'upd-sub' }, notes.map(function (n) { return typeof n === 'string' ? el('span', { text: n }) : n; }))
    ]);
    return el('li', { class: 'upd-item' }, [body, button]);
  }

  function renderUpdates(st) {
    var u = st.updates;
    $('updates').hidden = !u;
    if (!u) { return; }
    var show = !!u.available;
    var list = $('updates-list'), errs = $('updates-errors');
    clear(list);
    clear(errs);
    $('updates-card').hidden = !show;
    $('updates-ok').hidden = show;
    $('updates-title-text').textContent = updateCount(u) === 1 ? 'Update available' : updateCount(u) + ' updates available';

    // "checked …" is the last check that reached the network.
    var when = $('updates-checked');
    clear(when);
    if (u.checking) { when.appendChild(chip('checking…', 'accent')); }
    else if (u.checkedAt) { when.appendChild(document.createTextNode('checked ')); when.appendChild(agoSpan(u.checkedAt)); }
    else { when.textContent = 'not checked for updates yet'; }

    var problems = updateProblems(u);
    $('updates-ok-dot').className = 'dot ' + (u.checking || problems.errors.length ? 'dot-wait' : 'dot-on');
    $('updates-ok-text').textContent = u.checking ? 'Checking for updates…'
      : problems.errors.length ? 'Could not check for every update'
      : !u.checkedAt ? 'Not checked for updates yet'
      : problems.notes.length ? 'No updates found' : 'Everything is up to date';
    var okWhen = $('updates-ok-when');
    clear(okWhen);
    if (!u.checking && u.checkedAt) { okWhen.appendChild(document.createTextNode('· checked ')); okWhen.appendChild(agoSpan(u.checkedAt)); }
    document.querySelectorAll('[data-action="updates-check"]').forEach(function (b) {
      b.disabled = !!u.checking || !!updApplying;
      if (u.checking) { b.setAttribute('aria-busy', 'true'); } else { b.removeAttribute('aria-busy'); }
    });

    if (show) {
      if (u.app && u.app.available) {
        var appNotes = u.app.hint
          ? [el('span', null, ['This build upgrades from a terminal: ', el('code', { text: u.app.hint }), '.'])]
          : [el('span', null, ['Runs ', el('code', { text: 'go install' }), ' for the new release, then start ', el('code', { text: NAME + ' web' }), ' again to run it.'])];
        list.appendChild(updItem(NAME, bareVersion(u.app.current) + ' → ' + bareVersion(u.app.latest), appNotes,
          u.app.hint ? el('span', { class: 'upd-manual muted', text: 'from a terminal' }) : applyButton('app', 'Install update', 'Installing…')));
      }
      var cc = u.claudeCode;
      if (cc && cc.available) {
        var ccNotes = [];
        if (cc.detail) { ccNotes.push(cc.detail); }
        if (cc.command) { ccNotes.push(el('span', null, ['Runs ', el('code', { text: cc.command }), '.'])); }
        ccNotes.push('Running Claude Code sessions keep the old version until you restart them.');
        list.appendChild(updItem('Claude Code', (cc.installed || '?') + ' → ' + (cc.latest || 'latest'), ccNotes,
          applyButton('claude-code', 'Update Claude Code', 'Updating…'), cc.method));
      }
      if (!list.firstChild) {
        list.appendChild(el('li', { class: 'upd-sub', text: 'Something can be updated.' }));
      }
    }
    problems.errors.forEach(function (parts) { (show ? list : errs).appendChild(el('li', { class: 'upd-err' }, parts)); });
    problems.notes.forEach(function (parts) { (show ? list : errs).appendChild(el('li', { class: 'upd-note' }, parts)); });
    errs.hidden = !errs.firstChild;
    renderUpdateResult();
  }

  function renderUpdateResult() {
    var box = $('updates-result');
    box.hidden = !updResult;
    if (!updResult) { return; }
    box.className = 'notice upd-result' + (updResult.ok ? '' : ' notice-crit');
    box.setAttribute('role', updResult.ok ? 'status' : 'alert');
    $('updates-result-text').textContent = updResult.message;
    box.querySelector('[data-action="updates-dismiss"]').hidden = !!updResult.pending;
    $('updates-result-more').hidden = !updResult.output;
    $('updates-result-output').textContent = updResult.output || '';
  }

  // updateAsk: the confirmation for one target.
  function updateAsk(u, target) {
    if (target === 'app') {
      var v = bareVersion(u.app && u.app.latest);
      return {
        title: 'Install ' + NAME + ' ' + v + '?', ok: 'Install update', doing: 'Installing ' + NAME + ' ' + v + '…',
        message: 'Runs go install for the new release. This server keeps running the old version until you stop it and start ' + NAME + ' web again.'
      };
    }
    var cc = u.claudeCode || {};
    return {
      title: 'Update Claude Code?', ok: 'Update Claude Code', doing: 'Updating Claude Code. This can take a minute.',
      message: 'Update Claude Code from ' + (cc.installed || 'the installed version') + ' to ' + (cc.latest || 'the latest version') +
        (cc.command ? ' with ' + cc.command : '') + '. Running Claude Code sessions keep the old version until you restart them.'
    };
  }

  // ---- foldable cards (DESIGN A27) --------------------------------------------
  // The Accounts and the Updates card fold to their heading, which keeps its
  // count line (and the Accounts card its Add current login button and the
  // callout for a login that is not stored yet). The server remembers them
  // (state.ui.folded; the dashboard's port changes at every start, so the
  // browser could not); a fold this page sent wins until the state agrees.

  var FOLDABLE = ['accounts-card', 'updates-card'];
  var foldSent = {}; // card → folded, sent and not yet in the state

  function applyFolds(st) {
    var saved = (st && st.ui && st.ui.folded) || null;
    FOLDABLE.forEach(function (id) {
      var want = foldSent[id];
      if (saved && (want === undefined || !!saved[id] === want)) {
        delete foldSent[id];
        want = !!saved[id];
      }
      setFolded(id, !!want);
    });
  }

  // foldCard folds or unfolds a card at once and has the server remember it.
  function foldCard(id, folded) {
    foldSent[id] = folded;
    setFolded(id, folded);
    if (!state || !state.ui) { return Promise.resolve(); } // nothing remembers it: this page only
    return api('POST', '/api/ui/folded', { card: id, folded: folded }).catch(function (err) {
      toast('Could not remember that: ' + (err && err.message ? err.message : err));
    });
  }

  function setFolded(id, folded) {
    var card = $(id);
    if (!card) { return; }
    card.classList.toggle('folded', folded);
    var b = card.querySelector('.card-toggle');
    if (b) {
      b.setAttribute('aria-expanded', folded ? 'false' : 'true');
      b.title = folded ? 'Show' : 'Hide';
    }
  }

  // fallbackNotices: one notice while Claude Code is on an API-key account,
  // which is billed per token. Auto-switch never moves onto or off one by
  // itself (DESIGN A33): that changes how Claude Code authenticates.
  function fallbackNotices(st) {
    var out = [];
    var active = claudeRows(st).filter(function (a) { return a.isActive; })[0];
    if (active && active.kind === 'api_key') {
      var where = active.baseUrl ? 'Requests go to ' + hostOf(active.baseUrl) + '. ' : '';
      out.push(el('div', { class: 'notice notice-crit', role: 'alert' }, [
        el('b', { text: 'Running on API-key account #' + active.number + ' — billed per token. ' + where }),
        el('span', { text: 'Auto-switch leaves this alone; switch back by hand, then restart your Claude Code sessions.' })
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
    if (active.kind === 'api_key') {
      who.appendChild(active.baseUrl
        ? chip('API key · ' + hostOf(active.baseUrl), 'crit', 'Claude Code sends its requests to ' + active.baseUrl)
        : chip('API key · billed per token', 'crit', 'Claude Code is on a managed API-key account'));
    }
    box.appendChild(who);
    var u = active.usage || {};
    var meters = el('div', { class: 'hdr-meters' });
    meters.appendChild(u.fiveHour ? meter({ label: '5h', pct: u.fiveHour.pct, resetsAt: u.fiveHour.resetsAt, size: 'compact' }) : emptyMeter('5h', 'compact'));
    meters.appendChild(u.sevenDay ? meter({ label: '7d', pct: u.sevenDay.pct, resetsAt: u.sevenDay.resetsAt, size: 'compact' }) : emptyMeter('7d', 'compact'));
    var scoped = (u.scoped || []).slice();
    var top = scoped.sort(function (x, y) { return (pctNum(y.pct) || 0) - (pctNum(x.pct) || 0); })[0];
    if (top || !st.groupId) { meters.appendChild(top ? meter({ label: top.name || 'model', pct: top.pct, resetsAt: top.resetsAt, size: 'compact' }) : emptyMeter('model', 'compact')); }
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
      box.appendChild(tile('Best candidate', '—', [res.ranked.length ? 'no account is under every one of its bars' : 'no other account'], res.ranked.length ? 'warn' : ''));
    }
  }

  // ---- accounts ------------------------------------------------------------

  function statusChips(a) {
    var out = [];
    if (a.isActive) { out.push(chip('active', 'active')); }
    if (a.atLimit) { out.push(chip('at limit', 'crit', (a.limitingWindows || []).join(', ') || 'a window is at 100%')); }
    if (a.disabled) { out.push(chip('disabled', 'outline', 'held out of auto-rotation')); }
    if (a.groupBlocker) { out.push(chip(a.groupBlocker, 'outline')); }
    if (!a.switchable) { out.push(chip('not switchable', 'warn', isClaude(a) ? 'missing stored credentials or config backup' : 'a Codex API-key login cannot be switched to')); }
    if (a.usageStatus && SENTINEL_STATUSES[a.usageStatus] && a.usageStatus !== 'api_key') { out.push(chip(SENTINEL_STATUSES[a.usageStatus], 'serious')); }
    if (a.usageStatus === 'unavailable') { out.push(chip('usage unavailable', 'warn', 'the last measurement is stale or failed; the meters show the last good values')); }
    return out;
  }

  function usageFreshness(a) {
    if (!a.usageFetchedAt && !a.usageRefresh) { return null; }
    var refresh = a.usageRefresh || {};
    var parts = [];
    if (a.usageFetchedAt) { parts.push('Usage checked ' + new Date(a.usageFetchedAt).toLocaleTimeString()); }
    if (refresh.error) {
      parts.push(/429/.test(refresh.error) ? 'refresh rate-limited' : 'refresh failed');
    }
    if (refresh.nextAt) { parts.push('next attempt ' + new Date(refresh.nextAt).toLocaleTimeString()); }
    return el('span', { class: refresh.error ? 'chip chip-warn' : 'org', text: parts.join(' · '), title: 'Usage is cached between requests. Reloading the page respects the polling schedule and retry delay.' });
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

  // codexRow is one Codex account in the account table: what the terminal
  // dashboard offers on a Codex row (switch, disable / enable, remove) and no
  // more. Codex has no per-model windows, so the model cell stays empty, and
  // no token status or base URL.
  function codexRow(a) {
    var k = rowKey(a);
    var tr = el('tr', { class: (a.isActive ? 'active-row ' : '') + (a.disabled ? 'disabled-row' : ''), 'data-key': k });
    tr.appendChild(el('td', { class: 'c-slot' }, [el('span', { class: 'slot-chip', text: '#' + a.number, 'aria-label': 'codex slot ' + a.number })]));

    var acct = el('div', { class: 'acct' });
    var nameRow = el('div', { class: 'acct-name' }, [el('span', { class: 'primary', text: a.alias || a.email || '(no email)', title: a.email || '' })]);
    statusChips(a).forEach(function (c) { nameRow.appendChild(c); });
    acct.appendChild(nameRow);
    var meta = el('div', { class: 'acct-meta' }, [chip('codex', 'kind')]);
    if (a.kind) { meta.appendChild(chip(a.kind === 'api_key' ? 'api key' : a.kind, 'kind')); }
    if (a.alias && a.email) { meta.appendChild(el('span', { class: 'org', text: a.email, title: a.email })); }
    if (a.orgName) { meta.appendChild(el('span', { class: 'org', text: a.orgName, title: a.orgName })); }
    var freshness = usageFreshness(a);
    if (freshness) { meta.appendChild(freshness); }
    acct.appendChild(meta);
    tr.appendChild(el('td', { class: 'c-acct' }, [acct]));

    var u = a.usage || null;
    tr.appendChild(el('td', { class: 'c-meter c-m5' }, [u && u.fiveHour ? meter({ label: '5h', pct: u.fiveHour.pct, resetsAt: u.fiveHour.resetsAt }) : emptyMeter('5h')]));
    tr.appendChild(el('td', { class: 'c-meter c-m7' }, [u && u.sevenDay ? meter({ label: '7d', pct: u.sevenDay.pct, resetsAt: u.sevenDay.resetsAt }) : emptyMeter('7d')]));
    tr.appendChild(el('td', { class: 'c-meter c-mm' }));
    if (showTokenStatus) { tr.appendChild(el('td', { class: 'c-token token-cell', text: '—' })); }

    var acts = el('div', { class: 'row-actions' });
    var sw = el('button', { type: 'button', class: 'btn btn-sm btn-primary', 'data-post': '/api/switch/' + keyPath(a), 'data-label': 'Switch', 'aria-label': 'Switch to Codex account ' + a.number, text: a.isActive ? 'Active' : 'Switch' });
    if (a.isActive || !a.switchable) { sw.disabled = true; }
    acts.appendChild(sw);
    var menu = el('details', { class: 'menu' });
    menu.appendChild(el('summary', { class: 'btn btn-sm btn-icon', 'aria-label': 'More actions for Codex account ' + a.number, 'aria-haspopup': 'menu', text: '⋯' }));
    var listEl = el('div', { class: 'menu-list', role: 'menu' });
    if (a.disabled) {
      listEl.appendChild(menuButton('Enable for rotation', { 'data-post': '/api/accounts/' + keyPath(a) + '/enable', 'data-label': 'Enable' }));
    } else {
      listEl.appendChild(menuButton('Disable (hold out of rotation)', { 'data-post': '/api/accounts/' + keyPath(a) + '/disable', 'data-label': 'Disable' }));
    }
    listEl.appendChild(el('div', { class: 'menu-sep' }));
    listEl.appendChild(menuButton('Remove…', { 'data-action': 'remove', 'data-id': k, 'data-name': accountLabel(a), danger: true }));
    menu.appendChild(listEl);
    acts.appendChild(menu);
    tr.appendChild(el('td', { class: 'c-actions td-actions' }, [acts]));
    return tr;
  }

  function renderAccounts(st) {
    var body = $('accounts-body');
    clear(body);
    var list = displayAccounts(st, accountOrder);
    var codex = codexRows(st);
    $('accounts-tbl').classList.toggle('no-model-column', st.groupId === 'opus' && !groupModels(st, 'opus').length);
    $('accounts-empty').hidden = list.length > 0 || codex.length > 0;
    // A login Claude Code has that is not stored yet is the next account to
    // add (A27): the callout says so, and the Add current login button in
    // the card's head is the main action either way.
    var cur = st.currentLogin;
    var unsaved = !!(cur && cur.email && !cur.saved);
    $('add-callout').hidden = !unsaved;
    $('add-callout-email').textContent = unsaved ? cur.email : '';
    $('add-hint').hidden = unsaved || list.length === 0;
    $('accounts-tbl').hidden = list.length === 0 && codex.length === 0;
    // Add current Codex login: on a machine with Codex accounts (the server
    // then runs the Codex engine too).
    $('add-current-codex').hidden = !((st.auto && st.auto.codex) || codex.length);
    var count = codex.length ? list.length + ' Claude \u00b7 ' + codex.length + ' Codex' : list.length + (list.length === 1 ? ' account' : ' accounts');
    $('accounts-sub').textContent = count + (st.activeNumber !== null && st.activeNumber !== undefined ? ' · active #' + st.activeNumber : ' · none active');
    var accIgnored = accountGroup ? null : ignoredModelsNote(st, parseModelNames(settingValue(st, 'autoswitch.model')));
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
      if (a.baseUrl) { meta.appendChild(chip('\u2192 ' + hostOf(a.baseUrl), 'kind', 'Requests go to ' + a.baseUrl)); }
      if (a.alias && a.email) { meta.appendChild(el('span', { class: 'org', text: a.email, title: a.email })); }
      if (a.orgName) { meta.appendChild(el('span', { class: 'org', text: a.orgName, title: a.orgName })); }
      var freshness = usageFreshness(a);
      if (freshness) { meta.appendChild(freshness); }
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
      if (a.kind === 'api_key') {
        // A change of how Claude Code authenticates: ask first (DESIGN A33).
        sw.setAttribute('data-action', 'switch-api-key');
        sw.setAttribute('data-id', rowKey(a));
        sw.setAttribute('data-name', accountLabel(a));
        if (a.baseUrl) { sw.setAttribute('data-endpoint', a.baseUrl); }
      } else {
        sw.setAttribute('data-post', accountGroup ? '/api/groups/' + accountGroup + '/switch/' + encodeURIComponent(a.number) : '/api/switch/' + keyPath(a));
      }
      if (a.isActive || !a.switchable || a.groupBlocker) { sw.disabled = true; }
      acts.appendChild(sw);

      var menu = el('details', { class: 'menu' });
      menu.appendChild(el('summary', { class: 'btn btn-sm btn-icon', 'aria-label': 'More actions for account ' + a.number, 'aria-haspopup': 'menu', text: '⋯' }));
      var listEl = el('div', { class: 'menu-list', role: 'menu' });
      var k = rowKey(a);
      if (!accountGroup) { listEl.appendChild(menuButton('Force switch (no backup)', { 'data-action': 'force-switch', 'data-id': k, 'data-name': accountLabel(a), disabled: a.isActive })); }
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
    if (codex.length) {
      body.appendChild(el('tr', { class: 'provider-head' }, [el('th', { colspan: '7', text: 'Codex' })]));
      codex.forEach(function (a) { body.appendChild(codexRow(a)); });
    }
  }

  function renderGroupChoice(st) {
    var control = $('account-group');
    if (!control) { return; }
    if (accountGroup !== 'fable' && accountGroup !== 'opus') { accountGroup = ''; }
    control.value = accountGroup;
    var g = (st.groups || []).filter(function (v) { return v.id === accountGroup; })[0];
    $('account-group-note').textContent = g
      ? (g.blocker || ((g.liveSessions || 0) + (g.liveSessions === 1 ? ' session · ' : ' sessions · ') + (g.activeNumber ? 'account #' + g.activeNumber : 'choose an account when starting a session')))
      : 'Existing sessions keep their current profile. Choose a group on restart or resume.';
    document.querySelectorAll('[data-action="switch-strategy"]').forEach(function (b) { b.disabled = !!accountGroup; });
    document.querySelectorAll('[data-sort]').forEach(function (b) {
      var active = b.getAttribute('data-sort') === accountOrder.key;
      var label = b.getAttribute('data-label') || b.textContent;
      if (b.getAttribute('data-sort') === 'model') {
        var names = accountGroup ? groupModels(st, accountGroup) : modelWindowNames(st);
        label = names.length === 1 ? names[0].charAt(0).toUpperCase() + names[0].slice(1) : 'Model limits';
      }
      b.setAttribute('data-label', label);
      b.textContent = label + (active ? (accountOrder.direction === 1 ? ' ↑' : ' ↓') : '');
      b.classList.toggle('selected', active);
      if (b.parentNode.tagName === 'TH') { b.parentNode.setAttribute('aria-sort', active ? (accountOrder.direction === 1 ? 'ascending' : 'descending') : 'none'); }
    });
  }
  if ($('account-group')) {
    $('account-group').addEventListener('change', function (ev) {
      accountGroup = ev.target.value;
      if (accountOrder.key === 'model' && accountGroup === 'opus' && !groupModels(state, 'opus').length) { accountOrder = { key: 'best', direction: 1 }; }
      try { sessionStorage.setItem('accountGroup', accountGroup); } catch (e) {}
      if (state) { render(state); }
    });
  }
  document.addEventListener('click', function (ev) {
    var b = ev.target.closest('[data-sort]');
    if (!b) { return; }
    var key = b.getAttribute('data-sort');
    accountOrder = { key: key, direction: key !== 'best' && accountOrder.key === key ? -accountOrder.direction : 1 };
    if (state) { render(state); }
  });

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
      c.group ? chip(c.group === 'fable' ? 'Fable' : 'Opus / other', 'accent', 'Sessions in this group switch accounts together') :
        c.profile ? chip('run as #' + c.profile, 'accent') : chip('default login', 'outline'),
      c.model ? el('span', { class: 'cell-sub', text: c.model }) : null
    ]));
    tr.appendChild(el('td', { class: 'c-pid num mono', text: String(c.pid) }));
    tr.appendChild(el('td', { class: 'c-kind' }, [el('span', { text: c.kind || '—' }), c.entrypoint ? el('span', { class: 'cell-sub', text: ' · ' + c.entrypoint }) : null]));
    tr.appendChild(el('td', { class: 'c-status' }, [statusChip(c.status)]));
    tr.appendChild(el('td', { class: 'c-started cell-sub' }, [el('span', { 'data-started': String(c.startedAt || 0), text: '' })]));
    var stop = el('button', { type: 'button', class: 'btn btn-sm btn-danger', 'data-post': '/api/sessions/' + encodeURIComponent(c.pid) + '/stop', 'data-label': 'Stop', 'data-confirm': 'Stop Claude Code session ' + c.pid + (c.cwd ? ' in ' + c.cwd : '') + '? `claude --continue` in that directory resumes it.', 'aria-label': 'Stop session ' + c.pid, text: 'Stop' });
    var handover = el('button', { type: 'button', class: 'btn btn-sm', 'data-action': 'recovery-prepare', 'data-session': c.sessionId, 'data-provider': 'claude', text: 'Continue in Codex…', disabled: c.status === 'busy' || !c.sessionId });
    tr.appendChild(el('td', { class: 'c-actions td-actions' }, [handover, stop]));
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
    var strat = String(engineSetting(st, 'autoswitch.strategy') || 'soonest-reset');
    var models = parseModelNames(engineSetting(st, 'autoswitch.model'));
    if (!a) {
      badge.textContent = 'unavailable'; badge.className = 'chip chip-outline';
      body.appendChild(el('div', { class: 'empty-state' }, [el('span', { class: 'title', text: 'Auto-switch is not wired into this build' }), el('code', { text: NAME + ' auto' })]));
      actions.hidden = true;
      slider.disabled = true;
    } else {
      actions.hidden = false;
      if (!a.available) { badge.textContent = a.managedBy ? 'managed by ' + a.managedBy : 'engine unavailable'; badge.className = 'chip chip-warn'; }
      else if (a.running) { badge.textContent = a.dryRun ? 'running · dry-run' : 'running'; badge.className = 'chip ' + (a.dryRun ? 'chip-warn' : 'chip-good'); }
      else { badge.textContent = 'stopped'; badge.className = 'chip chip-outline'; }

      var tiles = el('div', { class: 'tiles' });
      tiles.appendChild(tile('State', a.managedBy ? 'managed externally' : a.running ? (a.dryRun ? 'dry-run' : 'running') : 'stopped', [a.running ? (a.dryRun ? 'decides, never switches' : 'switches near the limit') : a.managedBy ? a.managedBy : 'not polling'], a.running ? (a.dryRun ? 'warn' : 'good') : ''));
      tiles.appendChild(tile('7d threshold', Math.round(a.threshold * 10) / 10 + '%', ['the 5h and model windows have bars of their own']));
      tiles.appendChild(tile('Strategy', strat, [el('span', { class: 'ellipsis', text: countingNote(models), title: countingNote(models) })]));
      tiles.appendChild(tile('Started', a.startedAt ? el('span', { 'data-started': String(Math.round(a.startedAt * 1000)), text: fmtAgo(Date.now() - a.startedAt * 1000) }) : '—', [a.startedAt ? fmtUnix(a.startedAt) : 'not running']));
      if (a.codex) { tiles.appendChild(codexTile(a.codex)); }
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
    var barNote = '5h ' + Math.round(res.bars.fiveHour * 10) / 10 + '% · 7d ' + Math.round(res.bars.sevenDay * 10) / 10 + '%';
    if (res.models.length) { barNote += ' · model ' + Math.round(res.bars.model * 10) / 10 + '%'; }
    $('nextbest-sub').textContent = countingNote(res.models) + ' · ' + res.strategy + ' · ' + (res.models.length ? '7d → model → 5h' : '7d → 5h') + ' · switch at ' + barNote;
    var nbSub = $('nextbest-sub');
    var savedModels = parseModelNames(settingValue(st, 'autoswitch.model'));
    var ignored = ignoredModelsNote(st, savedModels);
    if (ignored) { nbSub.appendChild(document.createTextNode(' \u00b7 ')); nbSub.appendChild(ignored); }
    if (a && a.running && JSON.stringify(res.models) !== JSON.stringify(savedModels)) {
      nbSub.appendChild(document.createTextNode(' \u00b7 '));
      nbSub.appendChild(chip('running engine: ' + countingNote(res.models), 'outline', 'The running engine counts a different set than the saved setting; save autoswitch.model again to retarget it'));
    }
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
      else if (r.tier === 3) { verdict = chip(r.soonReset ? 'at limit · resets <5h' : 'at limit', r.soonReset ? 'accent' : 'crit', r.soonReset ? 'All counted full windows reset within five hours; not available yet' : ''); }
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

  // codexTile: the Codex engine that runs beside the Claude one (DESIGN A47).
  // Its one bar is fixed when it starts: the slider moves the Claude 7d bar
  // only.
  function codexTile(c) {
    var last = c.lastTick ? (c.lastTick.detail || c.lastTick.outcome) : 'no tick yet';
    var sub = c.enabled || c.running ? 'bar ' + Math.round(c.threshold * 10) / 10 + '%, fixed at start \u00b7 last: ' + last : 'autoswitch.codexEnabled is off';
    return tile('Codex engine', c.running ? 'running' : c.enabled ? 'stopped' : 'off', [el('span', { class: 'ellipsis', text: sub, title: sub })], c.running ? 'good' : '');
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
      var codex = ev.provider === 'codex'; // a Codex engine tick (DESIGN A47)
      var li = el('li', {}, [
        el('span', { class: 'stamp', text: ev.at ? fmtClock(ev.at) : '' }),
        el('span', {}, [chip(ev.kind || 'event', variant), codex ? chip('codex', 'kind') : null]),
        el('span', { class: 'msg' }, [ev.message || '', ev.account ? el('span', { class: 'acct-ref', text: ' · ' + (codex ? 'codex #' : '#') + ev.account }) : null])
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
      api('POST', '/api/auto/threshold', { threshold: v }).then(function () { toast('7d threshold ' + v + '% applied to the running engine.', 'ok'); }).catch(function (err) { toast(err.message); });
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

  // ---- model picker (pure; testdata/modelpicker.cjs runs this block) ------

  // modelPickerOptions: the boxes the Settings tab shows for autoswitch.model
  // (DESIGN A49): one per model window the accounts report, then one per name
  // the saved value carries that no account reports, ticked, so a save never
  // drops it. Names split on commas only and match without case, as
  // settings.ParseModelNames does; a box keeps the reported spelling. "all"
  // ticks the All box and leaves the named ones as they are.
  function modelPickerOptions(saved, reported) {
    var names = typeof saved === 'string' ? saved.split(',').map(function (x) { return x.trim(); }).filter(Boolean) : [];
    var all = false;
    var picked = {};
    names.forEach(function (n) {
      if (n.toLowerCase() === 'all') { all = true; } else { picked[n.toLowerCase()] = true; }
    });
    var seen = {};
    var options = [];
    function add(n, checked) {
      var k = n.toLowerCase();
      if (k === 'all' || seen[k]) { return; }
      seen[k] = true;
      options.push({ name: n, checked: checked });
    }
    (reported || []).forEach(function (n) { add(n, !!picked[n.toLowerCase()]); });
    names.forEach(function (n) { add(n, true); });
    return { all: all, options: options };
  }

  // modelPickerValue: what Save sends: "all", the ticked names joined by
  // commas, or null when nothing is ticked, which Save sends as a reset since
  // the server refuses an empty value.
  function modelPickerValue(all, ticked) {
    if (all) { return 'all'; }
    return ticked.length ? ticked.join(', ') : null;
  }

  // ---- end model picker ------------------------------------------------------

  // ---- settings (shared row renderer) ---------------------------------------

  var settingsScope = '';

  function settingsEndpoint(scope, key) {
    return (scope ? '/api/groups/' + encodeURIComponent(scope) : '/api') + '/settings/' + encodeURIComponent(key);
  }

  function settingsForScope(st, scope) {
    if (!scope) { return st.settings; }
    var group = (st.groups || []).filter(function (g) { return g.id === scope; })[0];
    return group && group.settingViews;
  }

  if ($('settings-scope')) {
    $('settings-scope').addEventListener('change', function (ev) {
      settingsScope = ev.target.value;
      modelSettingRow = null;
      delete renderSigs.settings;
      if (state) { renderSettings(state); }
    });
  }

  var SETTING_LABELS = {
    'autoswitch.fiveHourThreshold': '5h threshold', 'autoswitch.sevenDayThreshold': '7d threshold',
    'autoswitch.modelThreshold': 'Model threshold', 'autoswitch.intervalSeconds': 'Poll interval',
    'autoswitch.codexEnabled': 'Codex auto-switch', 'autoswitch.codexThreshold': 'Codex threshold',
    'autoswitch.cooldownSeconds': 'Cooldown', 'autoswitch.hysteresisPct': 'Hysteresis',
    'autoswitch.strategy': 'Strategy',
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

  // Persistent editor: state refreshes never replace an open control, an
  // unsaved draft, or a choice whose save is still in flight.
  function modelPicker(sv, id, reported, existing) {
    var box = existing || el('div', { class: 'model-picker', id: id, role: 'group' });
    var mode = box.querySelector('select');
    if (!mode) {
      mode = el('select', { 'aria-label': 'Count model limits' }, [
        el('option', { value: 'off', text: 'Off' }), el('option', { value: 'all', text: 'All models' }),
        el('option', { value: 'selected', text: 'Selected models' })
      ]);
      box.appendChild(mode);
    }
    var names = box.querySelector('.model-names') || el('div', { class: 'model-names' });
    if (!names.parentNode) { box.appendChild(names); }
    var signature = null;
    var named = [];
    var extra = null;
    function showNames() { names.hidden = mode.value !== 'selected'; }
    function changed() { box.modelDirty = true; showNames(); }
    box.addEventListener('change', changed);
    box.addEventListener('input', changed);
    box.pickerValue = function () {
      if (mode.value === 'off') { return null; }
      if (mode.value === 'all') { return 'all'; }
      var picked = named.filter(function (n) { return n.input.checked; }).map(function (n) { return n.name; });
      if (extra) { picked = picked.concat(extra.value.split(',').map(function (n) { return n.trim(); }).filter(Boolean)); }
      return modelPickerValue(false, picked);
    };
    box.sync = function (value, windows) {
      if (box.modelDirty || box.modelPending || box.contains(document.activeElement)) { return; }
      var pick = modelPickerOptions(value, windows);
      var next = JSON.stringify(pick.options.map(function (o) { return o.name; }));
      if (next !== signature) {
        clear(names);
        named = pick.options.map(function (o) {
          var cb = el('input', { type: 'checkbox', 'aria-label': 'Count the ' + o.name + ' window' });
          names.appendChild(el('label', { class: 'switch' }, [cb, el('bdi', { text: o.name })]));
          return { name: o.name, input: cb };
        });
        extra = el('input', { type: 'text', placeholder: 'Other model names, separated by commas', 'aria-label': 'Other model names' });
        names.appendChild(extra);
        signature = next;
      }
      named.forEach(function (n, i) { n.input.checked = pick.options[i].checked; });
      extra.value = '';
      mode.value = pick.all ? 'all' : pick.options.some(function (o) { return o.checked; }) ? 'selected' : 'off';
      showNames();
    };
    box.sync(sv.value, reported);
    return box;
  }

  function saveModelEditor(box, value, button) {
    if (box.modelPending) { return Promise.resolve(); }
    box.modelPending = true;
    var controls = Array.prototype.slice.call(box.querySelectorAll('input, select, button'));
    if (button && controls.indexOf(button) < 0) { controls.push(button); }
    controls.forEach(function (c) { c.disabled = true; });
    var url = '/api/settings/' + encodeURIComponent('autoswitch.model');
    return (value === null ? api('DELETE', url) : api('POST', url, { value: value })).then(function () {
      return api('GET', '/api/state' + (showTokenStatus ? '?tokenStatus=1' : ''));
    }).then(function (fresh) {
      box.modelDirty = false;
      box.modelPending = false;
      if (box.contains(document.activeElement) || document.activeElement === button) { document.activeElement.blur(); }
      delete renderSigs.settings;
      render(fresh);
      toast('Model windows saved.', 'ok');
    }).catch(function (err) {
      // Keep the attempted choice available for retry, including when the
      // save succeeded but the confirming read failed.
      box.modelDirty = true;
      toast(err.message);
    }).then(function () {
      box.modelPending = false;
      controls.forEach(function (c) { c.disabled = false; });
    });
  }

  function renderModelPicker(st) {
    var saved = settingValue(st, 'autoswitch.model');
    if (!autoModelPicker) {
      autoModelPicker = modelPicker({ value: saved }, 'auto-model-picker', modelWindowNames(st), $('auto-model-picker'));
      $('model-limits-save').addEventListener('click', function () {
        saveModelEditor(autoModelPicker, autoModelPicker.pickerValue(), this);
      });
    }
    autoModelPicker.sync(saved, modelWindowNames(st));
    $('model-limits-which').textContent = countingNote(parseModelNames(saved));
  }

  function settingControl(sv, reported) {
    var id = 'set-' + sv.key.replace(/[^a-z0-9]/gi, '-');
    var input = sv.key === 'autoswitch.model' ? modelPicker(sv, id, reported) : null;
    if (input) {
      input.setAttribute('aria-label', humanLabel(sv.key) + ' (' + sv.key + ')');
      return input;
    }
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
    if (input.pickerValue) { return input.pickerValue(); }
    if (sv.kind === 'bool') { return input.checked; }
    if (sv.kind === 'int' || sv.kind === 'float') { var n = parseFloat(input.value); return isNaN(n) ? input.value : n; }
    if (sv.key === 'autoswitch.model' && !input.value.trim()) { return null; }
    return input.value;
  }

  function fmtSetting(v, key) {
    if (v === null || v === undefined || v === '') { return 'empty'; }
    return String(v) + (typeof v === 'number' ? unitFor(key) : '');
  }

  function fmtDefault(sv) { return fmtSetting(sv.default, sv.key); }

  // settingRow: the key, what it does and when a saved value takes effect
  // (sv.applies, which the server words from what the code does), the
  // control, configured value and default, Save and Reset.
  function settingRow(sv, reported, scope) {
    if (!scope && sv.key === 'autoswitch.model' && modelSettingRow) {
      var editor = modelSettingRow.querySelector('.model-picker');
      if (editor.modelDirty || editor.modelPending || modelSettingRow.contains(document.activeElement)) { return modelSettingRow; }
    }
    var row = el('div', { class: 'setting-row', role: 'group', 'aria-label': sv.key });
    var idc = el('div', { class: 'setting-id' }, [
      el('div', { class: 'label-row' }, [el('span', { class: 'label', text: sv.label || humanLabel(sv.key) }), el('span', { class: 'key', text: sv.key, title: sv.key })]),
      sv.description ? el('div', { class: 'desc', text: sv.description }) : null,
      sv.applies ? el('div', { class: 'applies', text: sv.applies }) : null
    ]);
    row.appendChild(idc);
    if (sv.readOnly) {
      row.appendChild(el('div', { class: 'setting-ctl', text: fmtSetting(sv.value, sv.key) }));
      row.appendChild(el('div', { class: 'setting-default' }, [chip(sv.source === 'session' ? 'live sessions' : 'managed', 'outline')]));
      row.appendChild(el('div', { class: 'setting-actions muted', text: sv.readOnly }));
      return row;
    }
    var input = settingControl(sv, reported);
    var ctl = el('div', { class: 'setting-ctl' }, [input]);
    var unit = unitFor(sv.key);
    if (typeof sv.min === 'number' || typeof sv.max === 'number') {
      ctl.appendChild(el('span', { class: 'range-note', text: (typeof sv.min === 'number' ? sv.min : '…') + ' – ' + (typeof sv.max === 'number' ? sv.max : '…') + unit }));
    } else if (unit && sv.kind !== 'bool') {
      ctl.appendChild(el('span', { class: 'range-note', text: unit }));
    }
    row.appendChild(ctl);
    row.appendChild(el('div', { class: 'setting-default' }, [
      sv.isDefault ? chip(scope ? 'shared default' : 'default', 'outline') : chip(scope ? 'group override' : 'custom', 'accent'),
      el('span', { class: 'eff', text: 'configured ' + fmtSetting(sv.value, sv.key), title: 'configured value; application timing is described above' }),
      sv.isDefault ? null : el('span', { class: 'def', text: 'default ' + fmtDefault(sv), title: 'default value' })
    ]));
    var save = el('button', { type: 'button', class: 'btn btn-sm btn-primary', text: 'Save', 'aria-label': 'Save ' + sv.key });
    save.addEventListener('click', function () {
      var v = controlValue(input, sv);
      if (sv.key === 'autoswitch.model') { saveModelEditor(input, v, save); return; }
      var url = settingsEndpoint(scope, sv.key);
      run(save, 'Save ' + humanLabel(sv.key), v === null ? api('DELETE', url) : api('POST', url, { value: v }));
    });
    input.addEventListener('keydown', function (ev) { if (ev.key === 'Enter') { ev.preventDefault(); save.click(); } });
    var reset = el('button', { type: 'button', class: 'btn btn-sm btn-ghost', text: 'Reset', 'aria-label': 'Reset ' + sv.key + ' to default', disabled: !!sv.isDefault, title: 'Remove the override and fall back to the default' });
    reset.addEventListener('click', function () { if (sv.key === 'autoswitch.model') { saveModelEditor(input, null, reset); return; } run(reset, 'Reset ' + humanLabel(sv.key), api('DELETE', settingsEndpoint(scope, sv.key))); });
    row.appendChild(el('div', { class: 'setting-actions' }, [save, reset]));
    if (!scope && sv.key === 'autoswitch.model') { modelSettingRow = row; }
    return row;
  }

  // The settings editor is its own tab (A27): every key the settings
  // package defines, with its type, range, default and current value. It
  // needs only the settings facade (state.settings), never the auto engine:
  // the grid is hidden only when the server has no settings facade at all
  // (state.settings is null), which `tycswap web` always wires.
  function renderSettings(st) {
    var body = $('settings-grid');
    clear(body);
    var list = settingsForScope(st, settingsScope);
    var avail = Array.isArray(list);
    $('settings-empty').hidden = avail;
    $('settings-sub').textContent = avail ? list.length + ' keys · ' + list.filter(function (sv) { return !sv.isDefault; }).length + ' changed' : '';
    if (!avail) { return; }
    var reported = modelWindowNames(st);
    var sections = {};
    var order = [];
    list.forEach(function (sv) {
      var sec = sv.key.indexOf('.') > 0 ? sv.key.split('.')[0] : 'general';
      if (!sections[sec]) { sections[sec] = []; order.push(sec); }
      sections[sec].push(sv);
    });
    order.forEach(function (sec) {
      if (order.length > 1) { body.appendChild(el('h3', { class: 'settings-section-title', text: sec })); }
      sections[sec].forEach(function (sv) { body.appendChild(settingRow(sv, reported, settingsScope)); });
    });
  }

  // ---- guarded section rendering --------------------------------------------
  // A state event arrives every poll tick. Rebuilding a section's DOM on each
  // one destroyed whatever the user was typing into it (settings fields) and
  // reset open row menus and session groups. So
  // a section is repainted only when ITS slice of the state changed, never
  // while a control inside it has focus or a menu inside it is open (the
  // repaint is deferred until focus leaves / the menu closes), and open
  // <details> groups survive a rebuild.
  var renderSigs = {};
  var pendingRenders = {};

  // editingInside: a text-entry control, a form checkbox/radio, or a BUTTON
  // has focus inside the container, or a
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
        if (inForm && (t === 'checkbox' || t === 'radio')) { return true; }
        if (t !== 'checkbox' && t !== 'radio' && t !== 'range' && t !== 'button' && t !== 'submit') { return true; }
      }
    }
    return !!container.querySelector('details.menu[open]');
  }

  // sigOf: the change signature of a state slice. Fields that move every tick
  // without changing what is painted (usage freshness, server time, the event
  // ring the log consumes on its own path) are dropped, otherwise the guard
  // would repaint on every poll and defeat its purpose.
  var VOLATILE = { usageAgeSeconds: true, serverTime: true, events: true };
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
  document.addEventListener('focusout', function () { setTimeout(function () { flushPendingRenders(); if (state) { renderModelPicker(state); } }, 120); });
  document.addEventListener('click', function () { setTimeout(flushPendingRenders, 120); });
  document.addEventListener('toggle', function () { setTimeout(flushPendingRenders, 0); }, true);

  var pendingState = null;

  function renderRecovery(st) {
    var card = $('recovery-card');
    if (!card) { return; }
    var recovery = st.recovery || {};
    var incidents = (recovery.incidents || []).filter(function (x) { return x.decision.action !== 'ignore'; });
    var handovers = Object.keys(recovery.handovers || {}).map(function (key) { return recovery.handovers[key]; }).filter(function (x) { return x.status !== 'reclaimed'; });
    card.hidden = !incidents.length && !handovers.length && !recovery.error;
    $('recovery-wait').textContent = 'Offer handover when the wait exceeds ' + (recovery.waitMinutes || 30) + ' minutes';
    var box = $('recovery-list'); clear(box);
    if (recovery.error) { box.appendChild(el('p', { text: recovery.error })); }
    handovers.forEach(function (item) {
      var row = el('div', { class: 'recovery-row' });
      row.appendChild(el('strong', { text: lastSegment(item.cwd) + ' · continuing in ' + (item.destination.provider === 'codex' ? 'Codex' : 'Claude') }));
      row.appendChild(el('p', { text: item.destination_session_id ? 'Continuation started. The source conversation is preserved; its managed editing prompts are paused for this workspace.' : 'Waiting for the destination session to appear. This workspace stays reserved; another continuation will not be started.' }));
      box.appendChild(row);
    });
    incidents.forEach(function (item) {
      var event = item.event, decision = item.decision;
      var row = el('div', { class: 'recovery-row' });
      row.appendChild(el('strong', { text: (event.group === 'fable' ? 'Fable' : event.provider === 'codex' ? 'Codex' : 'Opus / other') + ' · ' + lastSegment(event.cwd) }));
      row.appendChild(el('p', { text: decision.reason }));
      if (decision.ready_at && Date.parse(decision.ready_at) > Date.now()) { row.appendChild(el('p', { text: 'Next known availability: ' + new Date(decision.ready_at).toLocaleString() })); }
      if (decision.action === 'offer') {
        row.appendChild(el('button', { type: 'button', class: 'btn btn-primary', 'data-action': 'recovery-prepare', 'data-session': event.session_id, 'data-provider': event.provider, text: 'Continue in ' + (event.provider === 'claude' ? 'Codex' : 'Claude') + '…' }));
        row.appendChild(el('button', { type: 'button', class: 'btn', 'data-action': 'recovery-dismiss', 'data-session': event.session_id, 'data-incident': event.incident_id, text: 'Wait' }));
      }
      box.appendChild(row);
    });
  }

  function prepareHandover(btn) {
    var id = btn.getAttribute('data-session'), provider = btn.getAttribute('data-provider');
    return openModal({ title: 'Prepare handover', message: 'The other tool starts a new conversation in the same workspace. The original session stays available.', okLabel: 'Review context', fields: [
      { name: 'objective', label: 'What should it continue?', hint: 'Optional. Saved recent messages and workspace changes are included.' },
      { name: 'constraints', label: 'Constraints or next steps' }
    ] }).then(function (values) {
      if (!values) { return null; }
      return api('POST', '/api/recovery/prepare', { sessionId: id, context: { objective: values.objective, constraints: values.constraints ? [values.constraints] : [] } });
    }).then(function (response) {
      if (!response) { return; }
      var packet = response.packet;
      var fields = [];
      if (provider === 'codex') { fields.push({ name: 'group', label: 'Claude group', type: 'select', value: 'opus', options: [{ value: 'opus', label: 'Opus / other' }, { value: 'fable', label: 'Fable' }] }); }
      fields.push({ name: 'pending', type: 'checkbox', label: 'I confirm the source is idle and no background command is still editing this workspace.', required: true });
      return openModal({ title: 'Review and continue in ' + (provider === 'claude' ? 'Codex' : 'Claude'),
        message: packet.omissions && packet.omissions.length ? 'Omitted: ' + packet.omissions.join(' · ') : 'Review the saved context below. The destination uses its own permissions.',
        preview: JSON.stringify(packet.source, null, 2), fields: fields, okLabel: 'Start continuation' }).then(function (choice) {
        if (!choice) { return; }
        var dest = provider === 'claude' ? { provider: 'codex', group: 'codex', model: '' } : { provider: 'claude', group: choice.group, model: choice.group };
        return api('POST', '/api/recovery/start', { packet: packet, destination: dest, reviewedDigest: packet.digest, explicit: true, pendingConfirmed: choice.pending }).then(function (result) {
          if (result.started) { toast(result.message, 'ok'); }
          else { return openModal({ title: 'Continue from a terminal', message: result.message, preview: JSON.stringify(result.plan, null, 2), okLabel: 'Close' }); }
        });
      });
    });
  }

  document.addEventListener('click', function (ev) {
    var btn = ev.target.closest('[data-action="recovery-prepare"], [data-action="recovery-dismiss"]');
    if (!btn) { return; }
    var operation = btn.getAttribute('data-action') === 'recovery-prepare' ? prepareHandover(btn) :
      api('POST', '/api/recovery/dismiss', { sessionId: btn.getAttribute('data-session'), incidentId: btn.getAttribute('data-incident') });
    operation.catch(function (err) { toast(err.message); });
  });

  function render(st) {
    if (st.sequence && st.sequence < minimumStateSequence) { return; }
    minimumStateSequence = Math.max(minimumStateSequence, st.sequence || 0);
    if (document.hidden) { pendingState = st; state = st; lastStateAt = Date.now(); return; } // paint once on return
    state = st;
    lastStateAt = Date.now();
    renderRecovery(st);
    renderGroupChoice(st);
    var accountView = groupState(st, accountGroup);
    renderModelPicker(st);
    renderHeader(accountView);
    renderOnboarding(st);
    renderGuarded('updates', 'updates', { updates: st.updates, applying: updApplying }, function () { renderUpdates(st); });
    renderGuarded('dashboard', 'accounts-card',
      { accounts: accountView.accounts, active: accountView.activeNumber, group: accountGroup, order: accountOrder, strategies: st.strategies, settings: st.settings, currentLogin: st.currentLogin,
        auto: st.auto && { running: st.auto.running, threshold: st.auto.threshold, settings: st.auto.settings, quarantine: st.auto.quarantine, codex: !!st.auto.codex } },
      function () { renderSummary(accountView); renderAccounts(accountView); });
    renderGuarded('auto', 'panel-auto',
      { auto: st.auto, accounts: st.accounts, active: st.activeNumber, settings: st.settings },
      function () { renderAuto(st); });
    renderGuarded('settings', 'panel-settings', { settings: settingsForScope(st, settingsScope), scope: settingsScope, models: modelWindowNames(st) }, function () { renderSettings(st); });
    renderGuarded('sessions', 'panel-sessions', st.sessions, function () { renderSessions(st.sessions); });
    applyFolds(st);
    tickCountdowns();
  }

  // ---- actions (delegated) ---------------------------------------------------

  function findAccount(id) {
    var list = (state && state.accounts) || [];
    for (var i = 0; i < list.length; i++) { if (rowKey(list[i]) === String(id)) { return list[i]; } }
    return null;
  }

  function closeMenus() { document.querySelectorAll('details.menu[open]').forEach(function (d) { d.removeAttribute('open'); }); }

  // API_KEY_SWITCH_NOTE is what the page says before a switch onto an API-key
  // account: it changes how Claude Code authenticates, so the server refuses
  // it unless the request carries the user's yes (DESIGN A33).
  var API_KEY_NOTE = 'This account authenticates with a key instead of a subscription login, and its usage is billed per token.';
  var API_KEY_SWITCH_NOTE = API_KEY_NOTE + ' Every Claude Code session that is already running keeps its current login until you restart it.';
  // endpointNote replaces the restart sentence for an account with a base URL
  // (DESIGN A46): where the requests go, the settings that take them there
  // and back, and that a running session takes the endpoint up when it
  // re-reads settings.json rather than only after a restart.
  function endpointNote(u) {
    return API_KEY_NOTE + ' This account sends Claude Code\'s requests to ' + u + ': the switch writes env.ANTHROPIC_BASE_URL and env.ANTHROPIC_AUTH_TOKEN into Claude Code\'s settings.json and removes env.ANTHROPIC_API_KEY, and a switch to another account puts back what they held. A Claude Code session that is already running takes the endpoint up when it re-reads settings.json (at once in a trusted workspace), or when it is restarted.';
  }

  // codexRestartNote: a Codex switch leaves the codex sessions that are
  // already running on the old account, and its result names them; the page
  // says so as `codex switch` and the terminal dashboard do (DESIGN A47).
  function codexRestartNote(res) {
    var r = res && res.result;
    if (r && Array.isArray(r.runningPids) && r.runningPids.length) {
      toast('codex is running (pid ' + r.runningPids.join(', ') + ') \u2014 restart it for the new account to take effect.', 'info');
    }
    return res;
  }

  var ACTIONS = {
    'card-toggle': function (btn) {
      var id = btn.getAttribute('data-card');
      return foldCard(id, !$(id).classList.contains('folded'));
    },
    'updates-show': function () {
      selectTab('dashboard');
      // Unfolding here is a fold change like the chevron's: sent, or the
      // next state folds the card again.
      if ($('updates-card').classList.contains('folded')) { foldCard('updates-card', false); }
      var card = $('updates-card').hidden ? $('updates-ok') : $('updates-card');
      if (card.scrollIntoView) { card.scrollIntoView({ block: 'start', behavior: 'smooth' }); }
      var title = $('updates-title');
      if (title && !$('updates-card').hidden) { title.focus({ preventScroll: true }); }
      return Promise.resolve();
    },
    'updates-check': function (btn) {
      btn.disabled = true; btn.setAttribute('aria-busy', 'true');
      return api('POST', '/api/updates/check', {}).catch(function (err) {
        toast(err && err.message ? err.message : String(err));
      }).then(function () {
        btn.disabled = false; btn.removeAttribute('aria-busy');
        if (state) { renderUpdates(state); }
      });
    },
    'updates-apply': function (btn) {
      var target = btn.getAttribute('data-target');
      var u = (state && state.updates) || {};
      var ask = updateAsk(u, target);
      return openModal({ title: ask.title, message: ask.message, okLabel: ask.ok }).then(function (v) {
        if (v === null) { return; }
        updApplying = target;
        updResult = { ok: true, pending: true, message: ask.doing };
        if (state) { renderUpdates(state); }
        return api('POST', '/api/updates/apply', { target: target }).then(function (res) {
          updResult = { ok: true, message: (res && res.message) || 'Done.', output: res && res.output };
        }).catch(function (err) {
          updResult = { ok: false, message: (err && err.message) || String(err), output: err && err.output };
        }).then(function () {
          updApplying = null;
          if (state) { renderUpdates(state); }
          return loadOnce().catch(function () {});
        });
      });
    },
    'updates-dismiss': function () { updResult = null; renderUpdateResult(); return Promise.resolve(); },
    'switch-strategy': function (btn) {
      var models = parseModelNames(settingValue(state, 'autoswitch.model'));
      var body = { strategy: strategy };
      if (models.length) { body.models = models; }
      return run(btn, 'Switch (' + strategy + ')', api('POST', '/api/switch', body));
    },
    // No confirmation (A27): storing the login Claude Code has changes
    // nothing else, and the callout above the table has already said what
    // the click does.
    'add-current': function (btn) {
      return run(btn, 'Add current login', api('POST', '/api/accounts/add', {}));
    },
    // The codex CLI's login, as `codex add` stores it (DESIGN A47).
    'add-current-codex': function (btn) {
      return run(btn, 'Add current Codex login', api('POST', '/api/accounts/add', { provider: 'codex' }));
    },
    'add-token': function (btn) {
      return openModal({
        title: 'Add account from token',
        message: 'Register a setup-token (sk-ant-oat01-…) or an API key (sk-ant-api…). With a base URL the token is that endpoint\'s API key, whatever it looks like. The token is sent once and never shown again.',
        fields: [
          { name: 'token', label: 'Token', type: 'password', required: true, placeholder: 'sk-ant-…' },
          { name: 'email', label: 'Email', type: 'email', placeholder: 'me@example.com', hint: 'Optional; identifies the account in lists.' },
          { name: 'slot', label: 'Slot', type: 'text', placeholder: 'next free', hint: 'Optional slot number.' },
          { name: 'alias', label: 'Alias', type: 'text', placeholder: 'work', hint: 'Optional short name.' },
          { name: 'baseUrl', label: 'Base URL', type: 'text', placeholder: 'gateway URL (http or https)', hint: 'Optional: the endpoint the key is for. A switch to the account points Claude Code at it; a switch away puts its settings back.' }
        ],
        okLabel: 'Add'
      }).then(function (v) {
        if (!v) { return; }
        var body = { token: v.token };
        if (v.email) { body.email = v.email; }
        if (v.slot) { body.slot = v.slot; }
        if (v.alias) { body.alias = v.alias; }
        if (v.baseUrl) { body.baseUrl = v.baseUrl; }
        return run(btn, 'Add token', api('POST', '/api/accounts/add-token', body));
      });
    },
    'switch-api-key': function (btn) {
      var id = btn.getAttribute('data-id');
      var endpoint = btn.getAttribute('data-endpoint');
      return confirmModal('Switch to API-key account ' + btn.getAttribute('data-name') + '?', (endpoint ? endpointNote(endpoint) : API_KEY_SWITCH_NOTE), 'Switch').then(function (ok) {
        if (!ok) { return; }
        return run(btn, 'Switch', api('POST', '/api/switch/' + encodeURIComponent(id) + '?confirmAuthChange=1'));
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
      var who = (a && !isClaude(a) ? 'Codex account ' : '') + btn.getAttribute('data-name');
      return confirmModal('Remove account', 'Remove ' + who + ' and its stored credentials from ' + NAME + '? ' + (a && a.isActive ? 'This is the ACTIVE account. ' : '') + 'This cannot be undone.', 'Remove').then(function (ok) {
        if (!ok) { return; }
        return run(btn, 'Remove', api('POST', '/api/accounts/' + encodeURIComponent(id) + '/remove'));
      });
    },
    'auto-start': function (btn) {
      var dry = btn.getAttribute('data-dry') === '1';
      return run(btn, dry ? 'Start dry-run' : 'Start', api('POST', '/api/auto/start', { dryRun: dry }));
    }
  };

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
      return run(btn, label, api('POST', url).then(codexRestartNote)).then(function () { delete inflight[url]; });
    });
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
    try { sessionStorage.removeItem('csrf'); } catch (e) { /* ignore */ }
    setConn('off');
    $('conn-text').textContent = 'session ended';
    toast('This dashboard session has ended. Run ' + NAME + ' web again and open the URL it prints (a new tab needs a fresh URL too).');
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

  if (!CSRF) {
    // No token: this tab was not opened from the launch URL (or storage was
    // cleared). Nothing here can succeed, so say what to do instead of
    // firing requests that answer 403.
    deadSession();
    $('conn-text').textContent = 'no session';
    return;
  }
  if (!window.EventSource) { loadOnce().catch(function (err) { toast(err.message); setConn('off'); }); }
  subscribe();
  setInterval(function () { if (!document.hidden) { tickCountdowns(); } }, 1000);
  document.addEventListener('visibilitychange', function () {
    if (!document.hidden) { tickCountdowns(); if (pendingState) { var ps = pendingState; pendingState = null; render(ps); } }
  });
})();
