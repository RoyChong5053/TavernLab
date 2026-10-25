// TavernLab frontend: vanilla JS, no build (rclone-friendly).
// One character = one self-contained data package; switching characters only
// re-reads that character's history, it never deletes chat data.
const $ = (s) => document.querySelector(s);
const store = {
  get(k, d) {
    try {
      let v = localStorage.getItem('tavernlab.' + k);
      if (v === null) v = localStorage.getItem('leerchat.' + k);
      return v === null ? d : JSON.parse(v);
    } catch { return d; }
  },
  set(k, v) { localStorage.setItem('tavernlab.' + k, JSON.stringify(v)); },
};

const settings = Object.assign(
  {
    context_window: 16384, reply_reserve: 4096, history_min_turns: 4,
    rerank_url: 'http://127.0.0.1:11437', char: 'Leer乐儿', model: '',
    stream: false, visible_turns: 10, user_name: 'RoyChong', avatar_px: 88,
    ui_scale: 100, theme: 'st-dark', theme_custom: {}, nav_collapsed: false,
  },
  store.get('settings', {}),
);

/* ---------- auth (Bearer, one-api style; optional server-side gate) ---------- */
// Token lives in sessionStorage by default, localStorage when "remember me".
// api() attaches it to every request and pops the login overlay on 401.
// EventSource / <img> / window.open can't send headers: withToken() appends
// ?token= instead (server accepts both).
let authEnabled = false, authUser = '';
function getToken() {
  try { return sessionStorage.getItem('tavernlab.token') || localStorage.getItem('tavernlab.token') || ''; }
  catch { return ''; }
}
function setToken(tok, remember) {
  try {
    if (remember) { localStorage.setItem('tavernlab.token', tok); sessionStorage.removeItem('tavernlab.token'); }
    else { sessionStorage.setItem('tavernlab.token', tok); localStorage.removeItem('tavernlab.token'); }
  } catch {}
}
function clearToken() {
  try { sessionStorage.removeItem('tavernlab.token'); localStorage.removeItem('tavernlab.token'); } catch {}
}
function authHeaders(h) {
  const o = Object.assign({}, h);
  const t = getToken();
  if (t) o['Authorization'] = 'Bearer ' + t;
  return o;
}
async function api(path, opts) {
  opts = opts || {};
  const r = await fetch(path, Object.assign({}, opts, { headers: authHeaders(opts.headers) }));
  // Only OUR gate rejection pops the login overlay. A 401 proxied from the
  // upstream (e.g. /api/models, /v1/chat/completions) must not log the user out.
  if (r.status === 401 && r.headers.get('X-Auth-Required') === '1') {
    showLogin('Session expired, please sign in again');
    throw new Error('HTTP 401 not signed in');
  }
  return r;
}
function withToken(url) {
  const t = getToken();
  if (!t || url.indexOf('token=') >= 0) return url;
  return url + (url.indexOf('?') >= 0 ? '&' : '?') + 'token=' + encodeURIComponent(t);
}
// imgURL appends ?token= to same-origin asset paths (<img> can't send headers).
function imgURL(u) {
  if (!u || u.startsWith('data:')) return u;
  return withToken(u);
}
function showLogin(msg) {
  const ov = $('#login-overlay');
  if (!ov) return;
  ov.classList.remove('hidden');
  $('#login-err').textContent = msg || '';
  if (authUser) $('#login-user').value = $('#login-user').value || authUser;
  setTimeout(() => ($('#login-pass') || $('#login-user')).focus(), 50);
}
async function doLogin() {
  const u = $('#login-user').value.trim(), p = $('#login-pass').value;
  const remember = $('#login-remember').checked;
  $('#login-err').textContent = '';
  try {
    const r = await fetch('/api/login', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ username: u, password: p, remember }),
    });
    const j = await r.json().catch(() => ({}));
    if (!r.ok || !j.ok) throw new Error((j && j.error) || ('HTTP ' + r.status));
    if (j.auth === 'disabled' || !j.token) { location.reload(); return; }
    setToken(j.token, remember);
    $('#login-pass').value = '';
    location.reload(); // simplest correct boot: token persisted, init reruns authed
  } catch (e) {
    $('#login-err').textContent = 'Sign-in failed: ' + (e.message || e);
  }
}
async function doLogout() {
  try { await fetch('/api/logout', { method: 'POST', headers: authHeaders({}) }); } catch {}
  clearToken();
  location.reload();
}
async function bootAuth() {
  try {
    const me = await (await fetch('/api/me', { headers: authHeaders({}) })).json();
    authEnabled = !!me.auth_enabled;
    authUser = me.user || '';
    const lo = $('#btn-logout');
    if (lo) lo.classList.toggle('hidden', !authEnabled);
    if (authEnabled && !me.ok) { showLogin(''); return false; }
  } catch { return true; }
  return true;
}

/* ---------- message formatting (markdown, latex, code highlight) ---------- */
let markdownConverter = null;
function initMarkdownConverter() {
  if (markdownConverter) return markdownConverter;
  if (typeof showdown === 'undefined') {
    console.warn('[TavernLab] showdown not loaded, markdown disabled');
    return null;
  }
  markdownConverter = new showdown.Converter({
    emoji: true,
    literalMidWordUnderscores: true,
    parseImgDimensions: true,
    tables: true,
    underline: true,
    simpleLineBreaks: true,
    strikethrough: true,
    disableForced4SpacesIndentedSublists: true,
    extensions: [
      // Custom extension: single underscore for italic (but not inside code)
      {
        type: 'output',
        regex: /(<code(?:\s+[^>]*)?>[\s\S]*?<\/code>|<style(?:\s+[^>]*)?>[\s\S]*?<\/style>)|\b(?<!_)_(?!_)(.*?)(?<!_)_(?!_)\b/gi,
        replace: function (match, tagContent, italicContent) {
          if (tagContent) return match;
          if (italicContent) return '<em>' + italicContent + '</em>';
          return match;
        }
      },
      // Custom extension: add data-lang to pre tags from code class
      {
        type: 'output',
        regex: /<pre><code class="language-([a-z0-9#+.-]+)">/gi,
        replace: '<pre data-lang="$1"><code>'
      },
      // Remove language-* class from code tag
      {
        type: 'output',
        regex: /<code class="language-[a-z0-9#+.-]+">/gi,
        replace: '<code>'
      }
    ]
  });
  return markdownConverter;
}

function formatMessage(text, isUser, isSystem) {
  if (!text) return '';

  // System messages: plain text only (escape HTML)
  if (isSystem) return escapeHtml(text);

  const converter = initMarkdownConverter();
  if (!converter) return escapeHtml(text);

  let html = text;

  // Assistant messages: full markdown + LaTeX
  // User messages: markdown only (no LaTeX to avoid accidental rendering)
  const renderLatex = !isUser;
  if (renderLatex) {
    html = html.replace(/\\begin\{align\*\}/g, '$$');
    html = html.replace(/\\end\{align\*\}/g, '$$');
    // Money guard (ported from App markdown_math.dart): a lone $ before a
    // number ("it costs $5 and $7") must not open a formula. Stash it as a
    // PUA placeholder OUTSIDE code spans only, restore after KaTeX (renderMath
    // skips pre/code tags, so code needs no guard and must keep raw $).
    // NOTE: `\$` escaping does NOT work — auto-render's left-delimiter regex
    // has no escape check and the backslash would stay visible.
    html = protectMoneyOutsideCode(html);
  }

  // Convert markdown to HTML
  html = converter.makeHtml(html);

  // Fix code blocks: showdown creates <br> in code blocks, normalize
  html = html.replace(/<code([^>]*)>[\s\S]*?<\/code>/g, function (match) {
    return match.replace(/\n/g, '\u0000');
  });
  html = html.replace(/\u0000/g, '\n');

  // Fix double-encoded entities inside code blocks (ST parity:
  // showdown escapes & to &amp; there; decode once back to &).
  html = html.replace(/<code([^>]*)>[\s\S]*?<\/code>/g, function (match) {
    return match.replace(/&amp;/g, '&');
  });

  // Sanitize with DOMPurify
  if (typeof DOMPurify !== 'undefined') {
    html = DOMPurify.sanitize(html, {
      RETURN_DOM: false,
      RETURN_DOM_FRAGMENT: false,
      RETURN_TRUSTED_TYPE: false,
      ADD_TAGS: ['custom-style'],
      ADD_ATTR: ['target', 'rel', 'data-lang']
    });
  }

  return html;
}

function escapeHtml(text) {
  const div = document.createElement('div');
  div.textContent = text;
  return div.innerHTML;
}

function renderMath(el) {
  if (typeof window.renderMathInElement === 'function') {
    try {
      window.renderMathInElement(el, {
        delimiters: [
          {left: '$$', right: '$$', display: true},
          {left: '$', right: '$', display: false},
          {left: '\\(', right: '\\)', display: false},
          {left: '\\[', right: '\\]', display: true}
        ],
        // Code stays code: $$ inside a fence must not become a formula
        // (App parity: markdown_widgets PreBlockBuilder owns fences).
        ignoredTags: ['script', 'noscript', 'style', 'textarea', 'pre', 'code', 'option'],
        throwOnError: false
      });
    } catch (e) {
      console.warn('[TavernLab] KaTeX render error:', e);
    }
  }
}

/* ---------- code highlight (hljs, no copy button by design) ---------- */
// Highlight.js is vendored locally (web/vendor/). Streaming frames skip it:
// highlighting is sync CPU work and re-running per chunk is what made long
// replies stutter in the App (markdown_widgets.dart). Final render only.
function highlightCode(el) {
  if (!el) return;
  try {
    if (typeof window.hljs !== 'undefined' && window.hljs.highlightElement) {
      el.querySelectorAll('pre code').forEach((c) => {
        if (c.dataset.hljs) return;
        c.dataset.hljs = '1';
        window.hljs.highlightElement(c);
      });
    }
  } catch (e) {
    console.warn('[TavernLab] hljs error:', e);
  }
  // Header row (language label + copy button) for every code block. Runs even
  // when hljs is missing so copy always works; streaming frames skip this
  // whole function by design (final render only).
  try {
    el.querySelectorAll('pre').forEach((pre) => {
      if (pre.querySelector(':scope > .code-head')) return;
      const code = pre.querySelector(':scope > code');
      if (!code) return;
      const head = document.createElement('div');
      head.className = 'code-head';
      const lang = document.createElement('span');
      lang.textContent = pre.dataset.lang || 'code';
      const btn = document.createElement('button');
      btn.type = 'button';
      btn.className = 'code-copy';
      btn.textContent = 'Copy';
      btn.title = 'Copy code';
      btn.onclick = (ev) => {
        ev.stopPropagation();
        copyText(code.innerText).then((ok) => {
          btn.textContent = ok ? 'Copied' : 'Failed';
          setTimeout(() => { btn.textContent = 'Copy'; }, 1500);
        });
      };
      head.append(lang, btn);
      pre.prepend(head);
    });
  } catch (e) {
    console.warn('[TavernLab] code-head error:', e);
  }
}

// Placeholder for money-$ while KaTeX runs (private-use, never typed).
const MONEY_PH = '\uE000';
function protectMoneyOutsideCode(text) {
  // Even segments = prose (guard), odd = code spans/fences (untouched).
  const parts = String(text).split(/(\s{0,3}```[\s\S]*?(?:```|$)|`[^`\n]*`)/g);
  for (let i = 0; i < parts.length; i += 2) {
    parts[i] = parts[i].replace(/(?<![$\\])\$(?=\d[\d,.]*(\s|$|[,.，。！？、]))/g, MONEY_PH);
  }
  return parts.join('');
}

// Single choke point for "rich" message bodies: markdown -> sanitize ->
// KaTeX -> hljs -> money restore. Every path (history / SSE /
// stream-complete / non-stream) must go through here so none renders
// half-styled.
function renderRichBody(el, text, isUser) {
  el.dataset.raw = text || '';
  el.innerHTML = formatMessage(text, isUser, false);
  // Tables need a scroll wrapper: styling overflow on the <table> itself would
  // require display:block, which breaks table layout.
  el.querySelectorAll('table').forEach((t) => {
    if (t.parentElement && t.parentElement.classList.contains('table-wrap')) return;
    const wrap = document.createElement('div');
    wrap.className = 'table-wrap';
    t.replaceWith(wrap);
    wrap.appendChild(t);
  });
  // External links leave the chat: open in a new tab with opener protection.
  el.querySelectorAll('a[href]').forEach((a) => {
    const h = a.getAttribute('href') || '';
    if (/^https?:/i.test(h)) { a.target = '_blank'; a.rel = 'noopener'; }
  });
  renderMath(el);
  highlightCode(el);
  if (el.innerHTML.indexOf(MONEY_PH) >= 0) {
    el.innerHTML = el.innerHTML.split(MONEY_PH).join('$');
  }
}

/* ---------- theme (custom colors; local-only preference) ---------- */
const THEMES = {
  'st-dark': { name: 'ST dark (default)' },
  'tavern-glass': { name: 'Tavern glass (legacy)' },
  'paper': { name: 'Paper (light)' },
  'custom': { name: 'Custom' },
};
function applyTheme() {
  const t = settings.theme || 'st-dark';
  document.body.dataset.theme = t;
  const c = settings.theme_custom || {};
  const root = document.documentElement;
  ['--bg', '--bg2', '--card', '--txt', '--mut', '--acc'].forEach((k) => {
    if (t === 'custom' && c[k]) root.style.setProperty(k, c[k]);
    else root.style.removeProperty(k);
  });
  const sel = $('#set-theme');
  if (sel && sel.value !== t) sel.value = t;
  const box = $('#theme-custom');
  if (box) box.classList.toggle('hidden', t !== 'custom');
  Object.keys(c).forEach((k) => {
    const el = document.querySelector(`[data-themek="${k}"]`);
    if (el && el.value !== c[k]) el.value = c[k];
  });
}
function setTheme(t) {
  settings.theme = THEMES[t] ? t : 'st-dark';
  store.set('settings', settings);
  applyTheme();
}
function setThemeCustom(k, v) {
  settings.theme_custom = Object.assign({}, settings.theme_custom, { [k]: v });
  store.set('settings', settings);
  if ((settings.theme || 'st-dark') === 'custom') {
    document.documentElement.style.setProperty(k, v);
  }
}

/* ---------- avatars (per-message + mood expressions) ---------- */
// Cached from GET /api/characters/:name: base avatar + expressions/ dir list.
// Mood flow: classifyAndBadge(label) -> expressions/<label>.webp|png|jpg|gif
// (character.go listExpressions; expression.go labels). Browsers play animated
// webp natively, so dynamic stickers need no code change — just drop files.
let charAvatarURL = '', charExpressions = [];
function pickExpressionURL(label) {
  if (!label) return '';
  const files = charExpressions || [];
  const lower = files.map((f) => String(f).toLowerCase());
  for (const ext of ['.webp', '.png', '.jpg', '.jpeg', '.gif']) {
    const i = lower.findIndex((f) => f.endsWith('/' + label + ext) || f === label + ext || f.endsWith(label + ext));
    if (i >= 0) return '/chars/' + encodeURIComponent(settings.char) + '/' + files[i];
  }
  return '';
}
function avatarFor(role, moodLabel) {
  if (role === 'user') return imgURL(userAvatarURL);
  if (moodLabel) {
    const u = pickExpressionURL(moodLabel);
    if (u) return imgURL(u);
  }
  return imgURL(charAvatarURL);
}
// User avatar element: uploaded photo, else initial block. Tagged with
// data-useravatar so upload/delete/rename can backfill live bubbles.
function makeUserAvatarEl() {
  if (userAvatarURL) {
    const img = document.createElement('img');
    img.className = 'avatar';
    img.alt = '';
    img.loading = 'lazy';
    img.dataset.useravatar = '1';
    img.src = imgURL(userAvatarURL);
    return img;
  }
  const u = document.createElement('div');
  u.className = 'avatar user-avatar';
  u.textContent = userInitial();
  u.title = settings.user_name || 'You';
  u.dataset.useravatar = '1';
  return u;
}
function refreshUserAvatarGutters() {
  document.querySelectorAll('#chat .avatar-gutter').forEach((g) => {
    if (g.querySelector('[data-useravatar]')) g.replaceChildren(makeUserAvatarEl());
  });
  const prev = $('#user-avatar-preview');
  if (prev) prev.src = imgURL(userAvatarURL) || BLANK_GIF;
}
async function loadUserAvatar() {
  try {
    const j = await (await api('/api/user/avatar/')).json();
    userAvatarURL = j.avatar_url || '';
  } catch { userAvatarURL = ''; }
  refreshUserAvatarGutters();
}
let userAvatarURL = '';
const BLANK_GIF = 'data:image/gif;base64,R0lGODlhAQABAAAAACH5BAEKAAEALAAAAAABAAEAAAICTAEAOw==';
function userInitial() {
  const n = (settings.user_name || 'You').trim();
  return (n ? [...n][0] : 'You').toUpperCase();
}
function fmtTime(t) {
  if (!t) return '';
  try {
    const d = new Date(String(t).replace(' ', 'T'));
    if (isNaN(d)) return String(t).slice(11, 16) || '';
    return d.toTimeString().slice(0, 5);
  } catch { return ''; }
}
/* ---------- ui scale (persisted, replaces browser 130% zoom) ---------- */
// Compensated transform instead of body.zoom: body.zoom + overflow:hidden used
// to clip the layout so the page could not scroll once zoomed in.
function applyUiScale() {
  const s = Math.min(150, Math.max(80, +settings.ui_scale || 100));
  const app = document.getElementById('app');
  if (app) {
    const z = s / 100;
    if (z === 1) {
      app.style.transform = '';
      app.style.width = '';
      app.style.height = '';
    } else {
      app.style.transformOrigin = 'top left';
      app.style.transform = `scale(${z})`;
      app.style.width = (100 / z) + '%';
      app.style.height = (100 / z) + 'vh';
    }
  }
  const el = $('#set-uiscale');
  if (el) el.value = s;
}

/* ---------- ui feedback: toast + async button helper ---------- */
function toast(msg, kind = 'ok', ms = 2600) {
  const w = document.getElementById('toast-wrap');
  if (!w) return;
  // Cap stacking at 3 and dedupe identical messages (prevents toast floods).
  const dup = [...w.children].find((x) => x.textContent === msg);
  if (dup) dup.remove();
  while (w.children.length >= 3) w.firstChild.remove();
  const d = document.createElement('div');
  d.className = 'toast ' + kind;
  d.setAttribute('role', 'status');
  d.textContent = msg;
  w.appendChild(d);
  setTimeout(() => { d.style.opacity = '0'; d.style.transition = 'opacity .2s'; setTimeout(() => d.remove(), 220); }, ms);
}
// Clipboard with a non-secure-context fallback (plain http over LAN has no
// navigator.clipboard). Returns true on success.
async function copyText(t) {
  try {
    await navigator.clipboard.writeText(t);
    return true;
  } catch {
    try {
      const ta = document.createElement('textarea');
      ta.value = t;
      ta.style.cssText = 'position:fixed;opacity:0;top:0;left:0';
      document.body.appendChild(ta);
      ta.select();
      const ok = document.execCommand('copy');
      ta.remove();
      return !!ok;
    } catch { return false; }
  }
}
// Styled confirm/prompt replacing native dialogs (themeable, Esc-aware).
function uiConfirm(msg) {
  return new Promise((resolve) => {
    const ov = document.createElement('div');
    ov.className = 'modal';
    ov.setAttribute('role', 'dialog');
    ov.setAttribute('aria-modal', 'true');
    ov.innerHTML = '<div class="modal-box glass" style="max-width:420px">' +
      '<div class="row" style="justify-content:space-between"><b>Please confirm</b></div>' +
      '<p style="font-size:14px;margin:8px 0"></p>' +
      '<div class="row" style="justify-content:flex-end"><button data-x="0">Cancel</button><button data-x="1" class="primary">OK</button></div></div>';
    ov.querySelector('p').textContent = msg;
    const onKey = (e) => { if (e.key === 'Escape') done(false); };
    const done = (v) => { document.removeEventListener('keydown', onKey, true); ov.remove(); resolve(v); };
    document.addEventListener('keydown', onKey, true);
    ov.addEventListener('click', (e) => {
      if (e.target === ov) done(false);
      const b = e.target.closest ? e.target.closest('[data-x]') : null;
      if (b) done(b.dataset.x === '1');
    });
    document.body.appendChild(ov);
    const okBtn = ov.querySelector('[data-x="1"]');
    if (okBtn) okBtn.focus();
  });
}
function uiPrompt(title, placeholder) {
  return new Promise((resolve) => {
    const ov = document.createElement('div');
    ov.className = 'modal';
    ov.setAttribute('role', 'dialog');
    ov.setAttribute('aria-modal', 'true');
    ov.innerHTML = '<div class="modal-box glass" style="max-width:420px">' +
      '<div class="row" style="justify-content:space-between"><b></b></div>' +
      '<input style="width:100%;margin:8px 0" placeholder="">' +
      '<div class="row" style="justify-content:flex-end"><button data-x="0">Cancel</button><button data-x="1" class="primary">OK</button></div></div>';
    ov.querySelector('b').textContent = title;
    const inp = ov.querySelector('input');
    inp.placeholder = placeholder || '';
    const onKey = (e) => {
      if (e.key === 'Escape') done(null);
      else if (e.key === 'Enter') done(inp.value.trim());
    };
    const done = (v) => { document.removeEventListener('keydown', onKey, true); ov.remove(); resolve(v); };
    document.addEventListener('keydown', onKey, true);
    ov.addEventListener('click', (e) => {
      if (e.target === ov) done(null);
      const b = e.target.closest ? e.target.closest('[data-x]') : null;
      if (b) done(b.dataset.x === '1' ? inp.value.trim() : null);
    });
    document.body.appendChild(ov);
    inp.focus();
  });
}
async function asyncAction(btn, fn) {
  if (btn) { btn.disabled = true; btn.classList.add('loading'); }
  try {
    const r = await fn();
    return r;
  } catch (e) {
    toast('Failed: ' + (e && e.message ? e.message : e), 'err', 4200);
    throw e;
  } finally {
    if (btn) { btn.disabled = false; btn.classList.remove('loading'); }
  }
}
/* ---------- "typing" indicator in the chat head ---------- */
let pendingTimer = null, pendingOn = false;
function setPending(on) {
  pendingOn = !!on;
  const el = document.getElementById('expr-badge');
  if (!el) return;
  clearInterval(pendingTimer);
  if (on) {
    const frames = ['Replying', 'Replying.', 'Replying..', 'Replying...'];
    let i = 0;
    el.classList.add('pending');
    el.classList.remove('pending-dots');
    el.textContent = frames[0];
    pendingTimer = setInterval(() => { i = (i + 1) % frames.length; el.textContent = frames[i]; }, 420);
  } else {
    el.classList.remove('pending');
    // mood text is restored by classifyAndBadge; fall back if it never resolves
    if (el.textContent.startsWith('Replying')) el.textContent = 'Mood · —';
  }
}

/* ---------- nav ---------- */
document.querySelectorAll('.nav button').forEach((b) => {
  b.onclick = () => {
    document.querySelectorAll('.nav button').forEach((x) => x.classList.remove('on'));
    b.classList.add('on');
    document.querySelectorAll('.page').forEach((p) => p.classList.remove('on'));
    document.getElementById('page-' + b.dataset.page).classList.add('on');
    document.body.classList.remove('nav-open');
    syncHamburger();
    if (b.dataset.page === 'audit') { refreshAudit(); refreshDistillAudit(); }
    if (b.dataset.page === 'chars') refreshChars();
    if (b.dataset.page === 'logs') openLogs(); else stopLogStream();
    if (b.dataset.page === 'gps') refreshGPS();
  };
  syncHamburger();
});
function syncHamburger() {
  const open = document.body.classList.contains('nav-open');
  const h = $('#hamburger');
  if (h) h.setAttribute('aria-expanded', open ? 'true' : 'false');
  const bd = $('#nav-backdrop');
  if (bd) bd.classList.toggle('hidden', !open);
}
$('#hamburger').onclick = () => { document.body.classList.toggle('nav-open'); syncHamburger(); };
{ const bd = $('#nav-backdrop'); if (bd) bd.onclick = () => { document.body.classList.remove('nav-open'); syncHamburger(); }; }
/* ---------- collapsible sidebar (desktop icon rail; state persists locally) ---------- */
function applyNavCollapsed() {
  const c = !!settings.nav_collapsed;
  document.body.classList.toggle('nav-collapsed', c);
  const b = $('#btn-nav-collapse');
  if (b) {
    b.title = c ? 'Expand sidebar' : 'Collapse sidebar';
    b.setAttribute('aria-label', b.title);
    b.setAttribute('aria-expanded', c ? 'false' : 'true');
  }
}
{
  const b = $('#btn-nav-collapse');
  if (b) b.onclick = () => {
    settings.nav_collapsed = !settings.nav_collapsed;
    store.set('settings', settings);
    applyNavCollapsed();
  };
}

/* ---------- health ---------- */
fetch('/api/health').then((r) => r.json()).then((h) => {
  const el = $('#health');
  el.textContent = '● ' + h.upstream;
  el.classList.add('ok');
}).catch(() => { $('#health').textContent = '● Backend offline'; });

/* ---------- prompt modules (Prompts-IDE) ---------- */
// The list of modules is fixed by the binary; the catalog from /api/modules
// drives labels, descriptions, placeholders and editability. Only text
// modules (system / time_anchor / note) expose an editable prompt.
let modules = [];
let moduleCatalog = [];
const MODULE_LEVELS = [
  { v: 1, t: 'L1 locked (never trimmed)' },
  { v: 2, t: 'L2 trimmable (evicted after floor)' },
  { v: 3, t: 'L3 elastic (dropped first)' },
];
function specFor(kind) { return moduleCatalog.find((s) => s.kind === kind) || null; }
async function loadModules() {
  try {
    const j = await (await api('/api/modules')).json();
    moduleCatalog = Array.isArray(j.catalog) ? j.catalog : [];
    modules = Array.isArray(j.modules) ? j.modules : [];
  } catch { moduleCatalog = []; modules = []; }
  renderModules();
}
let moduleSaveTimer = null;
function scheduleModuleSave() {
  setUnsaved(true);
  clearTimeout(moduleSaveTimer);
  moduleSaveTimer = setTimeout(() => saveModules(true), 700);
}
function setUnsaved(on) {
  const b = $('#btn-save-modules');
  const s = $('#modules-state');
  if (b) b.textContent = on ? 'Save •' : 'Save';
  if (s) s.textContent = on ? 'Unsaved changes…' : '';
}
async function saveModules(quiet) {
  clearTimeout(moduleSaveTimer);
  try {
    const r = await api('/api/modules', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ modules }) });
    if (!r.ok) throw new Error('HTTP ' + r.status);
    setUnsaved(false);
    if (!quiet) toast('Modules saved');
  } catch (e) {
    toast('Save failed: ' + e.message, 'err', 4200);
    setUnsaved(true);
  }
}
function moveModule(id, dir) {
  const ordered = [...modules].sort((a, b) => (a.order || 0) - (b.order || 0));
  const i = ordered.findIndex((m) => m.id === id);
  const j = i + dir;
  if (i < 0 || j < 0 || j >= ordered.length) return;
  const tmp = ordered[i]; ordered[i] = ordered[j]; ordered[j] = tmp;
  ordered.forEach((m, n) => { m.order = n; });
  modules = ordered;
  renderModules();
  scheduleModuleSave();
}
function renderModules() {
  const el = $('#modules');
  if (!el) return;
  el.innerHTML = '';
  const ordered = [...modules].sort((a, b) => (a.order || 0) - (b.order || 0));
  ordered.forEach((m, idx) => {
    const spec = specFor(m.kind) || {};
    const editable = m.kind === 'note' || !!spec.editable;
    const lvl = m.level || 1;
    const levelOpts = MODULE_LEVELS.map((x) => `<option value="${x.v}" ${x.v === lvl ? 'selected' : ''}>${x.t}</option>`).join('');
    const roleOpts = ['system', 'user', 'assistant'].map((r) => `<option value="${r}" ${r === m.role ? 'selected' : ''}>${r}</option>`).join('');
    const nameHTML = m.kind === 'note'
      ? `<input class="mod-title" data-id="${m.id}" data-k="title" value="${esc(m.title || '')}" placeholder="Module title">`
      : `<b>${esc(spec.title || m.id)}</b>`;
    const ph = (spec.placeholders || []).map((p) => `<code>${esc(p)}</code>`).join(' ');
    const d = document.createElement('div');
    d.className = 'module' + (m.enabled ? '' : ' off');
    d.dataset.id = m.id;
    d.innerHTML = `
      <div class="mod-hd">
        <label class="mod-check" title="${spec.imports_context ? 'Import context' : 'Enabled'}"><input type="checkbox" data-id="${m.id}" data-k="enabled" ${m.enabled ? 'checked' : ''}></label>
        <div class="mod-name">${nameHTML}</div>
        <span class="mod-actions">
          <button type="button" data-up="${m.id}" title="Move up" ${idx === 0 ? 'disabled' : ''}>↑</button>
          <button type="button" data-down="${m.id}" title="Move down" ${idx === ordered.length - 1 ? 'disabled' : ''}>↓</button>
          ${m.kind === 'note' ? `<button type="button" class="danger" data-del="${m.id}" title="Delete module">✕</button>` : ''}
        </span>
      </div>
      <div class="mod-badges">
        <span class="mod-badge ${spec.imports_context ? 'ctx' : ''}">${spec.imports_context ? 'Imports context' : 'Static text'}</span>
        <span class="mod-badge kind">${esc(m.kind)}</span>
        <span class="mod-badge">${m.enabled ? 'on' : 'off'}</span>
      </div>
      <div class="mod-desc meta">${esc(spec.description || '')}</div>
      ${editable ? `<textarea rows="3" data-id="${m.id}" data-k="template" placeholder="Prompt text">${esc(m.template || '')}</textarea>` : ''}
      ${ph ? `<div class="mod-ph">Available placeholders: ${ph}</div>` : ''}
      <details class="mod-advanced">
        <summary>Advanced</summary>
        <div class="mod-meta">
          <label>role <select data-id="${m.id}" data-k="role">${roleOpts}</select></label>
          <label>level <select data-id="${m.id}" data-k="level" title="Eviction tier">${levelOpts}</select></label>
          <label>priority <input type="number" data-id="${m.id}" data-k="evict_priority" value="${m.evict_priority ?? ''}" placeholder="auto" style="width:64px"></label>
          ${m.kind === 'web_search' ? `<label title="How many turns the raw search result stays in the context, counted from the turn the search ran on. 1 = that turn only; the vectorized copy carries it further.">keep turns <input type="number" min="1" max="20" data-id="${m.id}" data-k="keep_turns" value="${m.keep_turns || 1}" style="width:64px"></label>` : ''}
          ${m.collection ? `<span class="meta">collection: ${esc(m.collection)}</span>` : ''}
        </div>
      </details>`;
    el.appendChild(d);
  });
  el.querySelectorAll('input,textarea,select').forEach((inp) => {
    inp.onchange = () => {
      const m = modules.find((x) => x.id === inp.dataset.id);
      if (!m) return;
      const k = inp.dataset.k;
      if (k === 'enabled') m.enabled = inp.checked;
      else if (k === 'level') m.level = +inp.value;
      else if (k === 'role') m.role = inp.value;
      else if (k === 'title') m.title = inp.value;
      else if (k === 'evict_priority') {
        const v = inp.value.trim();
        if (v === '') delete m.evict_priority; else m.evict_priority = +v;
      }
      else if (k === 'keep_turns') {
        const v = +inp.value;
        m.keep_turns = v > 0 ? v : 1;
      } else m[k] = inp.value;
      scheduleModuleSave();
    };
  });
  el.querySelectorAll('[data-up]').forEach((btn) => { btn.onclick = () => moveModule(btn.dataset.up, -1); });
  el.querySelectorAll('[data-down]').forEach((btn) => { btn.onclick = () => moveModule(btn.dataset.down, 1); });
  el.querySelectorAll('[data-del]').forEach((btn) => {
    btn.onclick = async () => {
      const id = btn.dataset.del;
      if (!(await uiConfirm(`Delete module "${id}"?`))) return;
      modules = modules.filter((x) => x.id !== id);
      renderModules();
      scheduleModuleSave();
    };
  });
}
function ctxCfg() {
  return {
    context_window: settings.context_window,
    reply_reserve: settings.reply_reserve,
    history_min_turns: settings.history_min_turns,
  };
}

/* ---------- chat ---------- */
// Display window only: DOM holds the recent N turns; full context slides
// server-side out of the character's JSONL, so window size never affects the model.
// Scroll policy: stick-to-bottom only when the user is already near the bottom;
// loading earlier preserves anchor so refresh never jumps.
let historyShown = 0;
let userPinned = true;
const seenMsgIds = new Set();
// The in-flight assistant bubble. The server publishes the saved reply over SSE
// possibly before our own stream/response has finished rendering, so we adopt
// that event into this bubble instead of appending a duplicate.
let pendingAssistant = null;
// Bumped whenever an assistant message arrives over SSE, so the non-stream
// sender can tell "SSE already rendered my reply" from a legitimately repeated
// identical reply across turns.
let assistantSseSeq = 0;
function chatBox() { return document.querySelector('.chat-box'); }
function isNearBottom(box, px = 120) {
  if (!box) return true;
  return box.scrollHeight - box.scrollTop - box.clientHeight < px;
}
function scrollToBottom(box) {
  if (!box) return;
  box.scrollTop = box.scrollHeight;
}
// Unread pill: while the user is reading history, incoming messages bump a
// counter on the floating back-to-bottom button instead of yanking scroll.
let newMsgCount = 0;
function setNewCount(n) {
  newMsgCount = n;
  const s = $('#btn-bottom-n');
  if (s) {
    s.textContent = n > 0 ? (n > 99 ? '99+' : String(n)) : '';
    s.classList.toggle('hidden', !(n > 0));
  }
}
document.addEventListener('DOMContentLoaded', () => {
  const box = chatBox();
  if (box) {
    box.addEventListener('scroll', () => {
      userPinned = isNearBottom(box);
      const b = $('#btn-bottom');
      if (b) b.classList.toggle('hidden', userPinned);
      if (userPinned) setNewCount(0);
    });
  }
  const bb = $('#btn-bottom');
  if (bb) bb.onclick = () => { const b2 = chatBox(); if (b2) { scrollToBottom(b2); userPinned = true; bb.classList.add('hidden'); } setNewCount(0); };
});
// Image lightbox: tap any chat image for fullscreen, click/Esc to close.
function openLightbox(src) {
  const ov = $('#img-lightbox');
  const im = $('#img-lightbox-img');
  if (!ov || !im || !src) return;
  im.src = src;
  ov.classList.remove('hidden');
}
function closeLightbox() {
  const ov = $('#img-lightbox');
  if (ov) ov.classList.add('hidden');
  const im = $('#img-lightbox-img');
  if (im) im.removeAttribute('src');
}
document.addEventListener('click', (e) => {
  const t = e.target;
  if (t && t.id === 'img-lightbox') { closeLightbox(); return; }
  if (t && t.closest && t.closest('.img-preview')) return; // composer draft, not a message
  const img = t && t.closest ? t.closest('.msg-img, .msg .body img') : null;
  if (img && img.src) openLightbox(img.src);
});
document.addEventListener('keydown', (e) => {
  if (e.key === 'Escape') {
    closeLightbox();
    if (document.body.classList.contains('nav-open')) {
      document.body.classList.remove('nav-open');
      syncHamburger();
    }
  }
});
function renderMsg(role, text, who, prepend, images, id, time, mood) {
  const d = document.createElement('div');
  d.className = 'msg ' + (role === 'user' ? 'user' : role === 'assistant' ? 'ai' : 'sys');
  d.dataset.role = role;
  if (id) { d.dataset.id = id; seenMsgIds.add(id); }
  // Avatar gutter (ST-style row). User = initial block; assistant = char
  // avatar (mood expression when known); sys = none.
  if (role === 'user' || role === 'assistant') {
    const av = document.createElement('div');
    av.className = 'avatar-gutter';
    if (role === 'user') {
      av.appendChild(makeUserAvatarEl());
    } else {
      const img = document.createElement('img');
      img.className = 'avatar';
      img.alt = '';
      img.loading = 'lazy';
      img.dataset.base = '1'; // base avatar; mood swaps delete this flag
      img.src = avatarFor(role, mood) || BLANK_GIF;
      av.appendChild(img);
    }
    d.appendChild(av);
  }
  const content = document.createElement('div');
  content.className = 'msg-content';
  const w = document.createElement('div');
  w.className = 'who';
  const defaultWho = role === 'user' ? (settings.user_name || 'You') : role === 'assistant' ? settings.char : (role === 'distilled_memory' ? 'Distilled Memory' : role === 'web_search' ? 'Web Search' : 'System');
  const nameEl = document.createElement('span');
  nameEl.className = 'who-name';
  nameEl.textContent = who || defaultWho;
  w.appendChild(nameEl);
  const tt = fmtTime(time);
  if (tt) {
    const tEl = document.createElement('span');
    tEl.className = 'who-time';
    tEl.textContent = tt;
    w.appendChild(tEl);
  }
  // Per-message actions: copy raw source, regenerate (assistant), delete.
  // Desktop reveals on hover, touch always shows (see .msg-actions CSS).
  if (role === 'user' || role === 'assistant') {
    const acts = document.createElement('div');
    acts.className = 'msg-actions';
    const btnCopy = document.createElement('button');
    btnCopy.type = 'button';
    btnCopy.textContent = 'Copy';
    btnCopy.title = 'Copy raw text';
    btnCopy.onclick = () => {
      copyText(text || '').then((ok) => toast(ok ? 'Copied' : 'Copy failed', ok ? 'ok' : 'err'));
    };
    acts.appendChild(btnCopy);
    if (role === 'assistant') {
      const btnRe = document.createElement('button');
      btnRe.type = 'button';
      btnRe.textContent = 'Regen';
      btnRe.title = 'Regenerate the last reply (/regenerate)';
      btnRe.onclick = () => { send(true); };
      acts.appendChild(btnRe);
    }
    const btnDel = document.createElement('button');
    btnDel.type = 'button';
    btnDel.textContent = 'Delete';
    btnDel.title = 'Delete this message';
    btnDel.className = 'danger';
    btnDel.onclick = () => deleteMsg(d, id);
    acts.appendChild(btnDel);
    w.appendChild(acts);
  }
  const b = document.createElement('div');
  b.className = 'body';
  const isUser = role === 'user';
  const isSystem = role === 'sys';
  // Raw source for SSE dedupe: rendered textContent differs from source when
  // markdown is present (**x** -> x), so text-compare must use this, not DOM.
  b.dataset.raw = text || '';
  if (!text && images && images.length) {
    b.className += ' body-media-only';
  } else {
    renderRichBody(b, text, isUser);
  }
  content.append(w, b);
  if (images && images.length) {
    const im = document.createElement('div');
    im.className = 'msg-images';
    images.forEach((p) => {
      const img = document.createElement('img');
      img.className = 'msg-img';
      img.src = p.startsWith('data:') ? p : withToken('/chars/' + encodeURIComponent(settings.char) + '/' + p);
      im.appendChild(img);
    });
    content.appendChild(im);
  }
  d.appendChild(content);
  const box = $('#chat');
  const cbox = chatBox();
  if (prepend && box.firstChild) box.insertBefore(d, box.firstChild);
  else { box.appendChild(d); if (!prepend && (!cbox || userPinned)) d.scrollIntoView({ block: 'end' }); }
  return d;
}
function addMsg(role, text, who, images) { return renderMsg(role, text, who, false, images); }
// Delete one bubble persistently (server rewrites chat.jsonl) or DOM-only for
// local optimistic bubbles that carry no id yet.
async function deleteMsg(node, id) {
  if (!id) { node.remove(); return; }
  if (!(await uiConfirm('Delete this message? It is removed on the server too and will not reappear after a refresh.'))) return;
  try {
    const r = await api('/api/history?session=' + encodeURIComponent(settings.char) + '&id=' + encodeURIComponent(id), { method: 'DELETE' });
    if (!r.ok) throw new Error('HTTP ' + r.status + ' ' + (await r.text()).slice(0, 120));
    if (node.isConnected) node.remove();
    seenMsgIds.delete(id);
    historyShown = Math.max(0, historyShown - 1);
    toast('Deleted');
  } catch (e) { toast('Delete failed: ' + (e.message || e), 'err', 4200); }
}
async function loadHistory() {
  historyShown = 0;
  seenMsgIds.clear();
  $('#chat').innerHTML = '';
  await loadEarlier();
  const box = chatBox();
  if (box) { scrollToBottom(box); userPinned = true; }
}
async function loadEarlier() {
  if (loadEarlier.busy) return;
  loadEarlier.busy = true;
  const btn = $('#btn-earlier');
  const oldLabel = btn ? btn.textContent : '';
  if (btn) { btn.disabled = true; btn.textContent = 'Loading…'; }
  try {
    const q = new URLSearchParams({ session: settings.char, limit: settings.visible_turns, before: historyShown });
    const r = await api('/api/history?' + q);
    if (!r.ok) throw new Error('HTTP ' + r.status);
    const j = await r.json();
    const msgs = j.messages || [];
    if (!msgs.length) { if (btn) btn.textContent = 'No earlier messages'; return; }
    const chat = $('#chat');
    const cbox = chatBox();
    // Anchor preservation: keep the first visible message stable.
    const anchor = chat.firstChild;
    const oldTop = anchor ? anchor.getBoundingClientRect().top : 0;
    const oldScroll = cbox ? cbox.scrollTop : 0;
    [...msgs].reverse().forEach((m) => renderMsg(m.role, m.text, null, true, m.images, m.id, m.time));
    historyShown += msgs.length;
    if (cbox && anchor) {
      const newTop = anchor.getBoundingClientRect().top;
      cbox.scrollTop = oldScroll + (newTop - oldTop);
    }
    if (btn) btn.textContent = j.has_more ? '↑ Load earlier' : 'No earlier messages';
  } catch (e) {
    if (btn) btn.textContent = oldLabel || '↑ Load earlier (retry)';
    toast('Failed to load history: ' + (e.message || e), 'err', 4200);
  } finally {
    loadEarlier.busy = false;
    if (btn) btn.disabled = false;
  }
}
$('#btn-earlier').onclick = loadEarlier;
// Live updates from the server hub (other tabs / the app / background jobs).
// Ids de-dupe; text-compare absorbs the optimistic bubble from send().
let chatES = null;
function subscribeChat() {
  if (chatES) chatES.close();
  chatES = new EventSource(withToken('/api/events?session=' + encodeURIComponent(settings.char)));
  chatES.addEventListener('message', (ev) => {
    let m; try { m = JSON.parse(ev.data); } catch { return; }
    if (!m || !m.role) return;
    if (m.id && seenMsgIds.has(m.id)) return;
    const domRole = m.role === 'assistant' ? 'ai' : (m.role === 'user' ? 'user' : 'sys');
    // Our own reply can race the SSE event: absorb it into the in-flight bubble
    // (mid-stream or exact match) rather than adding a second identical bubble.
    if (m.role === 'assistant' && pendingAssistant) {
      const pb = pendingAssistant.querySelector('.body');
      if (pb && (pb.classList.contains('cursor') || (pb.dataset.raw || '') === (m.text || ''))) {
        // SSE delivers final message: full rich render (markdown+LaTeX+hljs)
        renderRichBody(pb, m.text, false);
        pb.classList.remove('cursor');
        const doneBox = pendingAssistant;
        if (m.id) { doneBox.dataset.id = m.id; seenMsgIds.add(m.id); }
        pendingAssistant = null;
        assistantSseSeq++;
        if (m.text) classifyAndBadge(m.text, doneBox);
        return;
      }
    }
    const bodies = [...document.querySelectorAll('#chat .msg.' + domRole + ' .body')];
    const lastBody = bodies[bodies.length - 1];
    // Only absorb into a LOCAL (id-less) bubble we rendered optimistically; a
    // server bubble already carries data-id, so a legitimately repeated
    // identical reply still renders as its own message. Compare dataset.raw:
    // rendered textContent loses markdown (**x** -> x) and never matches.
    if (lastBody && (lastBody.dataset.raw || '') === (m.text || '') && lastBody.parentElement && !lastBody.parentElement.dataset.id) {
      if (m.id) { lastBody.parentElement.dataset.id = m.id; seenMsgIds.add(m.id); }
      if (m.role === 'assistant') assistantSseSeq++;
      return;
    }
    renderMsg(m.role, m.text, null, false, m.images, m.id, m.time);
    // History readers stay put: bump the pill instead of scrolling.
    if (!userPinned) setNewCount(newMsgCount + 1);
    if (m.role === 'assistant') assistantSseSeq++;
    if (m.role === 'assistant' && m.text) {
      const boxes = [...document.querySelectorAll('#chat .msg.ai')];
      classifyAndBadge(m.text, boxes[boxes.length - 1] || null);
    }
  });
  chatES.onerror = () => { /* browser auto-reconnects */ };
}
async function classifyAndBadge(text, msgEl) {
  try {
    const r = await api('/api/expression/classify', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ text: text.slice(-500), rerank_url: settings.rerank_url }),
    });
    const j = await r.json();
    $('#expr-badge').textContent = j.fallback ? 'Mood · 😐 calm' : 'Mood · 😊 ' + j.label;
    $('#expr-badge').title = JSON.stringify(j.scores || {});
    // Mood avatar: swap this bubble's avatar to the expression file when the
    // character ships one (expressions/<label>.webp...). Preload to avoid
    // flashing; fall back silently to the base avatar.
    if (!j.fallback && j.label && msgEl) {
      const u = pickExpressionURL(j.label);
      if (u) {
        const probe = new Image();
        probe.onload = () => {
          const img = msgEl.querySelector('.avatar-gutter img.avatar');
          if (img) { img.src = imgURL(u); delete img.dataset.base; }
          // The head avatar follows the latest mood too.
          const head = $('#chat-avatar');
          if (head) head.src = imgURL(u);
        };
        probe.src = imgURL(u);
      }
    }
  } catch { /* silent: an expression failure must not interrupt chat */ }
}
/* ---------- image attach ---------- */
let pendingImages = [];
const MAX_IMG_MB = 8;
function addPendingImage(dataUrl, name) {
  if (!dataUrl || !dataUrl.startsWith('data:image/')) {
    toast('Unsupported image format' + (name ? ': ' + name : ''), 'err');
    return false;
  }
  // ~4/3 overhead for base64; reject loudly instead of silent drop (P0).
  const mb = (dataUrl.length * 3 / 4) / 1024 / 1024;
  if (mb > MAX_IMG_MB) {
    toast(`Image too large (~${mb.toFixed(1)}MB, max ${MAX_IMG_MB}MB) — compress and resend`, 'err', 4200);
    return false;
  }
  pendingImages = [dataUrl];
  renderImgPreview();
  return true;
}
function renderImgPreview() {
  const box = $('#img-preview');
  box.innerHTML = '';
  if (!pendingImages.length) { box.classList.add('hidden'); return; }
  box.classList.remove('hidden');
  pendingImages.forEach((d, i) => {
    const chip = document.createElement('div');
    chip.className = 'img-chip';
    chip.innerHTML = `<img src="${d}"><button title="Remove">✕</button>`;
    chip.querySelector('button').onclick = () => { pendingImages.splice(i, 1); renderImgPreview(); };
    box.appendChild(chip);
  });
}
$('#btn-attach').onclick = () => $('#img-file').click();
$('#img-file').onchange = async (e) => {
  const files = [...(e.target.files || [])];
  // Single image only: most vision APIs can't handle multiple. New pick replaces.
  const f = files.find((x) => x.type.startsWith('image/'));
  if (!f) { if (files.length) toast('Selected file is not an image', 'err'); }
  else if (f.size > MAX_IMG_MB * 1024 * 1024) {
    toast(`Image too large (${(f.size / 1048576).toFixed(1)}MB, max ${MAX_IMG_MB}MB)`, 'err', 4200);
  } else if (f) {
    try {
      const d = await new Promise((res, rej) => { const r = new FileReader(); r.onload = () => res(r.result); r.onerror = rej; r.readAsDataURL(f); });
      addPendingImage(d, f.name);
    } catch { toast('Failed to read image', 'err'); }
  }
  renderImgPreview();
  e.target.value = '';
};

// Slash-commands understood by the composer. Only exact matches are commands:
// any other text beginning with "/" is sent to the model as a normal message.
const REGEN_COMMANDS = new Set(['/regenerate', '/regen']);
function isRegenCommand(t) { return REGEN_COMMANDS.has((t || '').trim().toLowerCase()); }

/* ---------- /web_search command ---------- */
// The command is not a message. Typing it turns it into a chip above the
// composer, the way an attached photo looks, and the message that follows is
// what gets sent. The server turns the chip into an MCP web_search call and
// drops the raw result into the Web Search module.
//
//   /web_search                      on, no query: just add web_cache to recall
//   /web_search off                  take web_cache out of recall
//   /web_search on "some query"      search, and add web_cache to recall
const WEB_SEARCH_CMD = '/web_search';
let pendingWebSearch = null;   // {mode, query} — travels with the next message

// parseWebSearch reads a /web_search command out of the composer text.
// It returns null when the text is not a command.
function parseWebSearch(raw) {
  const t = (raw || '').trim();
  if (t.length < WEB_SEARCH_CMD.length) return null;
  if (t.slice(0, WEB_SEARCH_CMD.length).toLowerCase() !== WEB_SEARCH_CMD) return null;
  const rest0 = t.slice(WEB_SEARCH_CMD.length);
  // The command name has to end here: "/web_searchfoo" is not a command.
  if (rest0 && !/^[\s]/.test(rest0)) return null;
  const rest = rest0.trim();
  if (!rest) return { mode: 'on', query: '' };
  const m = rest.match(/^(\S+)\s*([\s\S]*)$/);
  const word = m[1].toLowerCase();
  if (word === 'on' || word === 'off') {
    return { mode: word, query: unquoteCmd(m[2].trim()) };
  }
  // No mode keyword: the rest is the query.
  return { mode: 'on', query: unquoteCmd(rest) };
}

function unquoteCmd(s) {
  s = (s || '').trim();
  if (s.length >= 2) {
    const a = s[0], b = s[s.length - 1];
    if ((a === '"' && b === '"') || (a === "'" && b === "'")) return s.slice(1, -1).trim();
  }
  return s;
}

// webSearchLabel is the chip's text.
function webSearchLabel(cmd) {
  if (!cmd) return '';
  if (cmd.mode === 'off') return 'web_search off';
  return cmd.query ? `web_search on · "${cmd.query}"` : 'web_search on';
}

// renderCmdPreview draws the chip. A preview (live, while typing) is shown
// dimmed; a committed one is the thing that travels with the next message.
function renderCmdPreview(liveText) {
  const box = $('#cmd-preview');
  if (!box) return;
  const live = liveText ? parseWebSearch(liveText) : null;
  const cmd = pendingWebSearch || live;
  if (!cmd) { box.classList.add('hidden'); box.innerHTML = ''; return; }
  box.classList.remove('hidden');
  const isLive = !pendingWebSearch && !!live;
  const icon = cmd.mode === 'off' ? '🔕' : '🔍';
  box.innerHTML = `<div class="cmd-chip${isLive ? ' live' : ''}">` +
    `<span class="cmd-label">${icon} ${esc(webSearchLabel(cmd))}</span>` +
    (isLive ? '' : '<button title="Remove">✕</button>') + '</div>';
  const btn = box.querySelector('button');
  if (btn) btn.onclick = () => { pendingWebSearch = null; renderCmdPreview(); };
}

async function send(regen = false) {
  const ta = $('#input');
  const text = ta.value.trim();
  // /regenerate: re-run the last user turn. The server drops the trailing
  // assistant reply first, so the partial leaves the context and a fresh
  // reply is generated. No new user bubble is added.
  if (!regen && isRegenCommand(text)) { await send(true); return; }
  // /web_search: a command, not a message. It becomes a chip above the
  // composer and waits there; the message typed next travels with it.
  if (!regen) {
    const cmd = parseWebSearch(text);
    if (cmd) {
      pendingWebSearch = cmd;
      ta.value = '';
      growComposer();
      renderCmdPreview();
      toast('/web_search ' + cmd.mode + (cmd.query ? ' · "' + cmd.query + '"' : '') + ' — now type your message and send');
      return;
    }
  }
  const imgs = regen ? [] : pendingImages.slice();
  // A pending /web_search chip travels with a message; the chip alone is not
  // a turn, so there has to be something to send.
  if (!regen && !text && !imgs.length) return;
  if (regen) {
    const ais = [...document.querySelectorAll('#chat .msg.ai')];
    const last = ais.pop();
    if (last) last.remove(); // replaced by the streamed reply
  } else {
    addMsg('user', text || '(image)', null, imgs);
  }
  ta.value = '';
  growComposer();
  if (!regen) { pendingImages = []; renderImgPreview(); }
  setPending(true);
  userPinned = true;
  const cbox = chatBox();
  if (cbox) scrollToBottom(cbox);
  const stream = !!settings.stream;
  const seqAtStart = assistantSseSeq;
  const maxTokens = Math.min(65536, Math.max(256, +settings.reply_reserve || 4096));
  const body = regen
    ? { model: settings.model || undefined, session: settings.char, regenerate: true, stream, modules, context: ctxCfg(), max_tokens: maxTokens }
    : { model: settings.model || undefined, session: settings.char, text, images: imgs, stream, modules, context: ctxCfg(), max_tokens: maxTokens };
  if (!regen && pendingWebSearch) {
    body.web_search = { mode: pendingWebSearch.mode, query: pendingWebSearch.query };
    pendingWebSearch = null;
    renderCmdPreview();
  }
  try {
      if (stream) {
      const r = await api('/v1/chat/completions', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
      if (!r.ok || !r.body) throw new Error('HTTP ' + r.status + ' ' + (await r.text()).slice(0, 200));
      const reader = r.body.getReader();
      const dec = new TextDecoder();
      let buf = '', full = '', finishReason = '', streamError = '';
      const box = addMsg('assistant', '');
      pendingAssistant = box;
      const belly = box.querySelector('.body');
      belly.classList.add('cursor');
      // Throttled paint: DOM writes at most ~11fps so long streams don't
      // stutter; the final rich render below still shows the full text.
      let lastPaint = 0;
      const paintStream = (force) => {
        const now = performance.now();
        if (!force && now - lastPaint < 90) return;
        lastPaint = now;
        belly.textContent = full;
        belly.dataset.raw = full;
        if (userPinned) { const cb = chatBox(); if (cb) scrollToBottom(cb); }
      };
      for (;;) {
        const { done, value } = await reader.read();
        if (done) break;
        buf += dec.decode(value, { stream: true });
        const parts = buf.split('\n\n');
        buf = parts.pop();
        for (const p of parts) {
          const line = p.trim();
          if (!line.startsWith('data:')) continue;
          const payload = line.slice(5).trim();
          if (!payload || payload === '[DONE]') continue;
          try {
            const j = JSON.parse(payload);
            if (j.error) streamError = (j.error.message || 'stream error');
            const fr = j.choices?.[0]?.finish_reason || '';
            if (fr) finishReason = fr;
            const delta = j.choices?.[0]?.delta?.content || '';
            if (delta) { full += delta; paintStream(false); }
          } catch (err) { console.warn('sse parse', err, payload); }
        }
      }
      paintStream(true);
      belly.classList.remove('cursor');
      // Stream complete: full rich render (markdown+LaTeX+hljs)
      if (full.trim()) {
        renderRichBody(belly, full, false);
      } else {
        belly.textContent = '(empty reply)';
      }
      pendingAssistant = null;
      setPending(false);
      if (streamError) toast('Upstream stream interrupted, reply may be incomplete: ' + streamError, 'err', 6000);
      else if (finishReason === 'length') toast('Reply truncated at the generation limit (finish=length) — raise Reply reserve', 'err', 5000);
      else if (finishReason === 'content_filter') toast('Reply cut by content filter (finish=content_filter)', 'err', 5000);
      if (full.trim()) classifyAndBadge(full, box);
      return;
    }
    const r = await api('/v1/chat/completions', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
    const j = await r.json().catch(() => ({}));
    if (!r.ok) {
      const detail = (typeof j === 'string' ? j : JSON.stringify(j)).slice(0, 300);
      if (r.status === 400 && /image/i.test(detail)) throw new Error('Image rejected: ' + detail);
      throw new Error('HTTP ' + r.status + ' ' + detail);
    }
    const reply = j.choices?.[0]?.message?.content || '(no content)';
    const finish = j.choices?.[0]?.finish_reason || '';
    if (finish === 'length') toast('Reply truncated (finish=length) — raise Reply reserve', 'err', 4200);
    else if (finish === 'content_filter') toast('Reply cut by content filter (finish=content_filter)', 'err', 4200);
    // SSE may have already rendered this reply (server publishes before our HTTP
    // response lands); only add the bubble if it isn't already the last one.
    // Compare dataset.raw: rendered textContent drops markdown and mismatches.
    const lastAiBox = [...document.querySelectorAll('#chat .msg.ai')].pop();
    const lastAiBody = lastAiBox ? lastAiBox.querySelector('.body') : null;
    const sseRenderedMine = lastAiBody && (lastAiBody.dataset.raw || '') === reply && assistantSseSeq > seqAtStart;
    let mineBox = null;
    if (!sseRenderedMine) mineBox = addMsg('assistant', reply);
    setPending(false);
    // Mood badge must update even when SSE won the race (mineBox null):
    // fall back to the last assistant bubble.
    if (!mineBox) {
      const boxes = [...document.querySelectorAll('#chat .msg.ai')];
      mineBox = boxes[boxes.length - 1] || null;
    }
    classifyAndBadge(reply, mineBox);
  } catch (e) {
    setPending(false);
    pendingAssistant = null;
    const box = addMsg('assistant', '⚠ Send failed: ' + (e.message || e), 'err');
    const btn = document.createElement('button');
    btn.className = 'retry';
    btn.textContent = 'Retry';
    btn.onclick = () => { box.remove(); ta.value = text; pendingImages = imgs; renderImgPreview(); send(); };
    box.querySelector('.body').appendChild(document.createElement('br'));
    box.querySelector('.body').appendChild(btn);
    toast('Send failed: ' + (e.message || e), 'err', 4200);
  }
}
$('#btn-send').onclick = () => asyncAction($('#btn-send'), send);
// Composer auto-grow (max ~200px, then inner scroll). Reset after send.
function growComposer() {
  const ta = $('#input');
  if (!ta) return;
  ta.style.height = 'auto';
  ta.style.height = Math.min(200, ta.scrollHeight) + 'px';
}
$('#input').addEventListener('input', () => {
  growComposer();
  // Live preview of a /web_search command: the chip appears while the user
  // is still typing, so it is obvious the text is a command and not a
  // message.
  renderCmdPreview($('#input').value);
});
$('#input').addEventListener('keydown', (e) => {
  if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); asyncAction($('#btn-send'), send); }
});
// paste image support
$('#input').addEventListener('paste', async (e) => {
  const items = [...(e.clipboardData?.items || [])].filter((i) => i.type.startsWith('image/'));
  if (!items.length) return;
  e.preventDefault();
  const f = items[0].getAsFile();
  if (f) {
    if (f.size > MAX_IMG_MB * 1024 * 1024) { toast('Pasted image too large — compress and resend', 'err'); return; }
    try {
      const d = await new Promise((res, rej) => { const r = new FileReader(); r.onload = () => res(r.result); r.onerror = rej; r.readAsDataURL(f); });
      addPendingImage(d, 'paste');
    } catch { toast('Failed to read image', 'err'); }
  }
  renderImgPreview();
});
$('#btn-save-modules').onclick = () => asyncAction($('#btn-save-modules'), async () => { await saveModules(false); });
$('#btn-reload-modules').onclick = () => asyncAction($('#btn-reload-modules'), async () => { await loadModules(); toast('Reloaded'); });
$('#btn-add-module').onclick = async () => {
  const raw = await uiPrompt('New module title', 'Author note');
  const title = (raw || '').trim();
  if (!title) return;
  const id = 'note_' + Date.now().toString(36) + Math.random().toString(36).slice(2, 6);
  const maxOrder = modules.reduce((m, x) => Math.max(m, x.order || 0), 0);
  modules.push({ id, title, kind: 'note', role: 'system', order: maxOrder + 1, enabled: true, level: 1, template: '' });
  renderModules();
  scheduleModuleSave();
};

/* ---------- export / archive (moved to settings) ---------- */
$('#btn-export').onclick = () => {
  const q = new URLSearchParams({ session: settings.char, user: settings.user_name || 'user' });
  window.open(withToken('/api/export?' + q), '_blank');
};
$('#btn-export-all').onclick = () => {
  const q = new URLSearchParams({ session: settings.char, user: settings.user_name || 'user', archives: '1' });
  window.open(withToken('/api/export?' + q), '_blank');
};
$('#btn-archive').onclick = async () => {
  if (!(await uiConfirm(`Archive the current floor for "${settings.char}" and start a new one? It is moved into the character package; chat data is kept.`))) return;
  const j = await (await api('/api/archive', {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ session: settings.char }),
  })).json();
  if (j.ok) { loadHistory(); } else alert('Archive failed.');
};

/* ---------- audit: dry-run compile + per-generation replay ---------- */
function esc(s) { return String(s ?? '').replace(/</g, '&lt;'); }
function moduleTable(rows) {
  return `<table><tr><th>module</th><th>tier</th><th>role</th><th>tokens</th><th>cut</th><th>note</th></tr>` +
    rows.map((b) => `<tr><td>${esc(b.id)}</td><td>L${b.level || '?'}</td><td>${esc(b.role)}</td><td>${b.tokens}</td><td>${b.truncated ? '✂' : ''}</td><td class="meta">${esc(b.note || '')}</td></tr>`).join('') + `</table>`;
}
function fmtNum(n) {
  return Number(n || 0).toLocaleString('en-US');
}
// Audit ids are timestamps: 20261026-063858.661. The dropdown shows them in a
// readable form; the value stays the raw id (that is the filename).
function fmtAuditID(id) {
  const m = /^(\d{4})(\d{2})(\d{2})-(\d{2})(\d{2})(\d{2})\.(\d{3})$/.exec(id || '');
  if (!m) return id || '';
  return `${m[1]}-${m[2]}-${m[3]} ${m[4]}:${m[5]}:${m[6]}.${m[7]}`;
}
function contentText(c) {
  if (typeof c === 'string') return c;
  if (Array.isArray(c)) {
    return c.map((p) => {
      if (typeof p === 'string') return p;
      if (p && p.type === 'text') return p.text || '';
      if (p && p.type === 'image_url') return '[image]';
      return '';
    }).filter(Boolean).join('\n');
  }
  return c == null ? '' : String(c);
}
function indent(s, p) {
  return String(s).split('\n').map((l) => p + l).join('\n');
}
function usageLine(up) {
  if (!up) return '';
  return Object.keys(up)
    .filter((k) => typeof up[k] === 'number' || typeof up[k] === 'string')
    .map((k) => `${k} ${typeof up[k] === 'number' ? fmtNum(up[k]) : up[k]}`)
    .join(' · ');
}
// The chat audit used to dump raw JSON. The request is now rendered as the
// messages the model actually saw, which is what a human can read.
function renderChatAuditText(a) {
  const raw = a.raw_request || {};
  const msgs = Array.isArray(raw.messages) ? raw.messages : [];
  const out = [];
  out.push('── REQUEST ─────────────────────────────');
  out.push(`model ${raw.model || a.model || '?'} · stream ${raw.stream ? 'yes' : 'no'} · max_tokens ${raw.max_tokens ?? a.max_tokens ?? '?'}`);
  for (const m of msgs) {
    out.push('');
    out.push(`── ${String(m.role || '?').toUpperCase()} ─────────────────────────`);
    out.push(contentText(m.content) || '(empty)');
  }
  if (a.reply_text) {
    out.push('');
    out.push('── REPLY ───────────────────────────────');
    out.push(a.reply_text);
  }
  if (a.stream_error) {
    out.push('');
    out.push('── STREAM ERROR ────────────────────────');
    out.push(a.stream_error);
  }
  const up = usageLine(a.upstream_usage);
  if (up) {
    out.push('');
    out.push('── USAGE ───────────────────────────────');
    out.push(up);
  }
  return out.join('\n');
}
// A distill run is one system message + one user message, then the attempts.
// This is the "what did the extractor actually receive" view.
function renderDistillAuditText(a) {
  const msgs = (a.request && a.request.messages) || [];
  const sys = msgs.find((m) => m && m.role === 'system');
  const usr = msgs.find((m) => m && m.role === 'user');
  const out = [];
  out.push(`OUTCOME ${String(a.outcome || '?').toUpperCase()}${a.error ? ' — ' + a.error : ''}`);
  out.push(
    `captured ${fmtNum(a.fresh_msgs)} msgs (index ${a.fresh_from ?? '?'} → ${a.fresh_to ?? '?'})` +
    ` · state ${fmtNum(a.state_chars)}c · recent ${fmtNum(a.recent_log_chars)}c` +
    ` · movement ${fmtNum(a.movement_chars)}c · timeline ${fmtNum(a.timeline_chars)}c` +
    ` · ${fmtNum(a.elapsed_ms)} ms`
  );
  out.push('');
  out.push('── SYSTEM (sent) ───────────────────────');
  out.push(contentText(sys && sys.content) || '(empty)');
  out.push('');
  out.push('── USER (sent) ─────────────────────────');
  out.push(contentText(usr && usr.content) || '(empty)');
  out.push('');
  out.push('── ATTEMPTS ────────────────────────────');
  const ats = a.attempts || [];
  if (!ats.length) out.push('(none)');
  for (const at of ats) {
    out.push(`#${at.n} finish=${at.finish_reason || '?'} · completion=${fmtNum(at.completion_tokens)} · reasoning=${fmtNum(at.reasoning_chars)}c · delta=${fmtNum(at.delta_chars)}c`);
    if (at.reasoning) {
      const r = at.reasoning.length > 3000 ? at.reasoning.slice(0, 3000) + '\n… [reasoning truncated for display; full text is in the audit file]' : at.reasoning;
      out.push(indent(r, '    '));
    }
  }
  out.push('');
  out.push('── RESPONSE ────────────────────────────');
  out.push(a.delta || '(empty)');
  return out.join('\n');
}

// Refresh always re-reads data/audit from disk. That is what makes an rclone
// backup + folder clear show up immediately instead of leaving stale ids that
// then fail to load.
async function refreshAudit() {
  const sel = $('#audit-list');
  const prev = sel.value;
  let j = {};
  try { j = await (await api('/api/audit')).json(); } catch (e) { j = {}; }
  const ids = Array.isArray(j.ids) ? j.ids : [];
  sel.innerHTML = ids.map((id) => `<option value="${esc(id)}">${esc(fmtAuditID(id))}</option>`).join('');
  if (!ids.length) {
    $('#audit-summary').textContent = '';
    $('#audit-modules').innerHTML = '';
    $('#audit').textContent = 'No audit records on disk. If you just moved data/audit away with rclone, this is expected — Refresh re-reads the folder every time.';
    return;
  }
  sel.value = ids.includes(prev) ? prev : ids[0];
  await loadAudit(sel.value, false);
}
async function viewAudit() {
  const id = $('#audit-list').value;
  if (id) await loadAudit(id, true);
}
async function loadAudit(id, allowRefresh) {
  const r = await api('/api/audit/' + encodeURIComponent(id));
  if (!r.ok) {
    if (allowRefresh) { await refreshAudit(); return; }
    $('#audit').textContent = `Record ${fmtAuditID(id)} could not be read — it may have been removed from data/audit. Click Refresh to re-sync the list.`;
    return;
  }
  const a = await r.json();
  const mem = a.memory || {};
  const up = a.upstream_usage || {};
  const est = a.estimate_tokens ?? a.total_tokens;
  const act = a.actual_tokens || up.prompt_tokens || 0;
  const comp = a.completion_tokens || up.completion_tokens || 0;
  const fin = a.finish_reason || '';
  const truncWarn = fin === 'length' ? ' · <span style="color:#ffb4b4">⚠ reply truncated (finish=length) — raise Reply reserve</span>' : '';
  const overWarn = a.overflow ? ' · <span style="color:#ffb4b4">⚠ OVERFLOW (over input budget)</span>' : '';
  const memTxt = mem.enabled
    ? `Memory: ${esc(mem.collection || '')} · query="${esc((mem.query || '').slice(0, 60))}" · ${mem.chars ?? 0} chars${mem.error ? ' · ⚠ ' + esc(mem.error) : ''}`
    : 'Memory: disabled';
  $('#audit-summary').innerHTML =
    `<b>${a.total_tokens}</b> / ${a.budget_tokens} input tokens (estimate ${est} vs actual ${act || '?'}` +
    `${comp ? ' · completion ' + comp : ''} · max_tokens ${a.max_tokens || '?'}${fin ? ' · finish=' + esc(fin) : ''}` +
    `) · window ${a.window ? Math.round(a.window / 1024) + 'k' : '?'} · dropped: ${esc((a.dropped || []).join(', ') || 'none')}` +
    `${truncWarn}${overWarn}<br>${memTxt}` +
    `<br><span class="meta">Sliding window: input budget = window − reply reserve. L1 is never trimmed; L3 is dropped first; L2 is evicted by tier (RAG drops the lowest score, chat drops the oldest, keeping the floor).</span>`;
  $('#audit-modules').innerHTML = moduleTable(a.modules || []);
  $('#audit').textContent = renderChatAuditText(a);
}
$('#btn-audit-list').onclick = refreshAudit;
$('#btn-audit-view').onclick = viewAudit;

/* ---------- audit: distilled memory ---------- */
// One record per run, same id/folder model as the chat audits. The text view
// shows the exact system + user message and the attempts, so a failure can be
// diagnosed without guessing at settings or reading raw JSON.
async function refreshDistillAudit() {
  const sum = $('#distill-audit-summary');
  if (!sum) return;
  const sel = $('#distill-audit-list');
  const prev = sel ? sel.value : '';
  try {
    const j = await (await api('/api/audit/distilled?session=' + encodeURIComponent(settings.char))).json();
    const m = j.meta || {};
    const state = j.enabled ? `every ${j.interval} user turns` : 'auto-distill off';
    const last = m.last_run ? m.last_run.replace('T', ' ').replace(/\+.*$/, '') : 'never';
    const vec = j.vectorize_enabled ? `on → ${j.vectorize_collection}` : 'off';
    const lastEvt = (j.recent || []).slice(-1)[0];
    sum.innerHTML =
      `<b>${esc(j.session)}</b> · ${esc(state)} · last ok <b>${esc(last)}</b>` +
      ` · runs ${m.runs || 0} · latest attempt <b>${esc(lastEvt ? lastEvt.msg : '?')}</b>` +
      ` · ${j.days || 0} days / ${j.sheet_chars || 0} chars` +
      `<br>captured: ${j.fresh_msgs || 0} message(s), ${j.fresh_user_turns || 0} user turn(s)` +
      ` · last index ${m.last_index || 0}` +
      ` · vectorize ${esc(vec)}${m.last_vectorize_error ? ' · ⚠ ' + esc(m.last_vectorize_error) : ''}`;
    const params = $('#distill-audit-params');
    if (params) {
      params.textContent = [
        'model ' + (j.model || '(main chat model)'),
        'max_tokens ' + (j.max_tokens ?? '?'),
        'max input msgs ' + (j.max_input_msgs ?? '?'),
        'fact sheet cap ' + (j.max_chars ?? '?') + ' chars',
        'entry cap ' + (j.max_entry_chars ?? '?'),
        'recent logs ' + (j.recent_logs ?? '?'),
        'vectorize ' + (j.vectorize_enabled ? 'on' : 'off') + ' → ' + (j.vectorize_collection || '(none)'),
      ].join(' · ');
    }
    const ids = Array.isArray(j.ids) ? j.ids : [];
    if (sel) {
      sel.innerHTML = ids.map((id) => `<option value="${esc(id)}">${esc(fmtAuditID(id))}</option>`).join('');
      if (ids.length) {
        sel.value = ids.includes(prev) ? prev : ids[0];
        await loadDistillAudit(sel.value);
      } else {
        $('#distill-audit-text').textContent = 'No distill runs recorded yet. The first run writes one file per attempt to data/audit.';
      }
    }
    // Attempt history: meta.last_status only advances on success, so a failing
    // run would otherwise look like "everything is fine" here.
    const rec = $('#distill-audit-recent');
    if (rec) {
      const lines = (j.recent || []).map((e) => {
        const t = (e.time || '').replace('T', ' ').replace(/\+\d\d:\d\d$/, '');
        const f = e.fields || {};
        const pick = [];
        if (f.model) pick.push('model ' + f.model);
        if (f.max_tokens != null) pick.push('max_tokens ' + f.max_tokens);
        if (f.fresh_msgs != null) pick.push('msgs ' + f.fresh_msgs);
        if (f.prompt_chars != null) pick.push('prompt ' + f.prompt_chars + 'c');
        if (f.input_chars != null) pick.push('input ' + f.input_chars + 'c');
        if (f.finish_reason) pick.push('finish ' + f.finish_reason);
        if (f.completion != null) pick.push('completion ' + f.completion);
        if (f.delta_len != null) pick.push('delta ' + f.delta_len);
        if (f.reasoning_len != null) pick.push('reasoning ' + f.reasoning_len);
        if (f.added != null) pick.push('added ' + f.added + 'd');
        if (f.error) pick.push('error ' + f.error);
        return `${t} [${e.level}] ${e.msg} — ${pick.join(' · ')}`;
      });
      rec.textContent = lines.length ? lines.join('\n') : '(no distill activity in the log ring yet)';
    }
  } catch (e) {
    sum.textContent = 'Failed to load distilled-memory audit: ' + e.message;
  }
}
async function loadDistillAudit(id) {
  const pre = $('#distill-audit-text');
  if (!pre) return;
  const r = await api('/api/audit/distilled/' + encodeURIComponent(id));
  if (!r.ok) {
    pre.textContent = `Run ${fmtAuditID(id)} could not be read — it may have been removed from data/audit. Click Refresh to re-sync the list.`;
    return;
  }
  const a = await r.json();
  pre.textContent = renderDistillAuditText(a);
}
$('#btn-distill-audit-refresh') && ($('#btn-distill-audit-refresh').onclick = () => asyncAction($('#btn-distill-audit-refresh'), refreshDistillAudit));
$('#distill-audit-list') && ($('#distill-audit-list').onchange = () => loadDistillAudit($('#distill-audit-list').value));

/* ---------- logs ---------- */
let logSince = 0, logES = null;
const LOG_RANK = { debug: 0, info: 1, warn: 2, error: 3 };
function logThreshold() { return LOG_RANK[$('#log-level').value] ?? 1; }
function renderLogEntries(entries) {
  const view = $('#log-view');
  const th = logThreshold();
  for (const e of entries) {
    if ((LOG_RANK[e.level] ?? 1) < th) continue;
    const line = document.createElement('span');
    line.className = 'log-line log-' + e.level;
    const t = (e.time || '').replace('T', ' ').replace(/\+.*$/, '');
    const extra = e.fields ? ' ' + JSON.stringify(e.fields) : '';
    line.textContent = `[${t}] ${e.level.toUpperCase().padEnd(5)} ${e.msg}${extra}`;
    view.appendChild(line);
    logSince = Math.max(logSince, e.seq || 0);
  }
}
function stopLogStream() { if (logES) { logES.close(); logES = null; } }
async function openLogs() {
  stopLogStream();
  const j = await (await api('/api/logs?limit=400')).json();
  const view = $('#log-view');
  view.innerHTML = '';
  logSince = 0;
  renderLogEntries(j.entries || []);
  if (j.level) $('#log-level').value = j.level;
  $('#log-state').textContent = 'Live…';
  if ($('#log-live').checked) startLogStream();
}
function startLogStream() {
  stopLogStream();
  logES = new EventSource(withToken('/api/logs/stream'));
  logES.addEventListener('log', (ev) => {
    try { renderLogEntries([JSON.parse(ev.data)]); } catch {}
    if ($('#log-autoscroll').checked) { const v = $('#log-view'); v.scrollTop = v.scrollHeight; }
    $('#log-state').textContent = 'Live…';
  });
  logES.onerror = () => { $('#log-state').textContent = 'Disconnected, reconnecting…'; };
}
$('#btn-log-refresh').onclick = () => asyncAction($('#btn-log-refresh'), openLogs);
$('#log-live').onchange = () => { if ($('#log-live').checked) startLogStream(); else { stopLogStream(); $('#log-state').textContent = 'Paused'; } };
$('#log-level').onchange = async () => {
  await api('/api/logs', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ level: $('#log-level').value }) });
  openLogs();
};
$('#btn-log-copy').onclick = () => asyncAction($('#btn-log-copy'), async () => {
  await navigator.clipboard.writeText($('#log-view').innerText);
  toast('Log copied');
});


/* ---------- GPS-Logger page ---------- */
async function refreshGPS() {
  await Promise.all([refreshGT20(), refreshReitti()]);
}
async function refreshGT20() {
  try {
    const loc = await (await api('/api/location')).json();
    if (loc.ok) {
      const p = loc.latest || {}, g = loc.geocode || {};
      $('#gt20-render').textContent = '{{location}} → ' + (loc.render || '(empty)') +
        `\nlat ${p.lat}, ${p.lon} · acc ${p.acc || '?'}m · batt ${p.batt ?? '?'}% · provider ${p.provider || '?'}` +
        `\nplace: ${g.place || '?'}  [${(g.hierarchy || []).join(' > ')}]`;
    } else { $('#gt20-render').textContent = 'No location reported yet'; }
    const pts = await (await api('/api/location/points?cap=50')).json();
    const rows = (pts.points || []).slice(-20).map(p =>
      `${new Date(p.tst*1000).toLocaleString()}  ${p.lat},${p.lon}  acc=${p.acc||'?'}  batt=${p.batt ?? '?'}  ${p.provider||''}`);
    $('#gt20-points').textContent = rows.length ? rows.join('\n') : 'No points recorded';
    const jl = await (await api('/api/logs?limit=200')).json();
    const lines = (jl.entries || []).filter(e => /location|geocode|paikka/i.test(e.msg))
      .map(e => `[${(e.time||'').replace('T',' ').replace(/\+.*$/,'')}] ${e.level} ${e.msg} ${e.fields ? JSON.stringify(e.fields) : ''}`);
    $('#gt20-log').textContent = lines.length ? lines.join('\n') : 'No related log lines';
    $('#gt20-state').textContent = 'Updated ' + new Date().toLocaleTimeString();
  } catch (e) { $('#gt20-state').textContent = 'Load failed: ' + e.message; }
}
async function refreshReitti() {
  try {
    const j = await (await api('/api/reitti/movements?limit=30')).json();
    const rows = (j.movements || []).map(m =>
      `### ${m.ts || ''}  (session: ${m.session || '?'}, since: ${m.since || 'default'})\n${m.text || ''}`);
    $('#reitti-list').textContent = rows.length ? rows.join('\n\n') : 'No narrative stored yet (recorded when distill runs)';
    $('#reitti-state').textContent = 'Updated ' + new Date().toLocaleTimeString();
  } catch (e) { $('#reitti-state').textContent = 'Load failed: ' + e.message; }
}
$('#btn-gt20-refresh').onclick = () => asyncAction($('#btn-gt20-refresh'), refreshGT20);
$('#btn-reitti-refresh').onclick = () => asyncAction($('#btn-reitti-refresh'), refreshReitti);
$('#btn-reitti-live').onclick = () => asyncAction($('#btn-reitti-live'), async () => {
  const j = await (await api('/api/reitti/movement')).json();
  $('#reitti-live-out').textContent = j.ok ? (j.text || '(empty window)') : ('Error: ' + (j.error || 'unknown'));
});

/* ---------- chat avatar size (global, header + per-message) ---------- */
function applyAvatarSize() {
  const px = Math.min(240, Math.max(32, +settings.avatar_px || 88));
  document.documentElement.style.setProperty('--avatar-px', px + 'px');
  const h = $('#chat-avatar');
  if (h) { h.style.width = px + 'px'; h.style.height = px + 'px'; }
}
function syncChatHead() {
  $('#chat-char-name').textContent = settings.char;
  api('/api/characters/' + encodeURIComponent(settings.char)).then((r) => r.json()).then((j) => {
    charAvatarURL = j.avatar_url || '';
    charExpressions = j.expressions || [];
    $('#chat-avatar').src = imgURL(charAvatarURL) || BLANK_GIF;
    // History may have rendered before this fetch landed (selectChar races
    // syncChatHead vs loadHistory): backfill base avatars, leave mood ones.
    const fresh = imgURL(charAvatarURL);
    if (fresh) {
      document.querySelectorAll('#chat .msg.ai .avatar-gutter img.avatar[data-base]').forEach((im) => {
        if (im.src !== fresh) im.src = fresh;
      });
    }
    const ex = $('#char-expr');
    if (ex) ex.textContent = (charExpressions || []).length ? 'Expressions: ' + charExpressions.join(', ') : '';
  }).catch(() => {});
}

/* ---------- characters: self-contained packages ---------- */
let editingNew = false;
const EMPTY_AVATAR = 'data:image/gif;base64,R0lGODlhAQABAAAAACH5BAEKAAEALAAAAAABAAEAAAICTAEAOw==';
async function refreshChars() {
  const { characters } = await (await api('/api/characters')).json();
  const list = characters || [];
  const grid = $('#char-grid');
  if (!list.length) {
    grid.innerHTML = '<div class="meta">No characters yet — click "＋ New character" to start.</div>';
  } else {
    grid.innerHTML = list.map((c) => `
      <div class="char-card ${c.name === settings.char ? 'on' : ''}" data-name="${c.name}">
        <img class="avatar sq" src="${imgURL(c.avatar_url) || EMPTY_AVATAR}" alt="">
        <div>
          <div class="cc-name">${c.name}${c.orphan ? ' <span class="badge">Legacy · no card</span>' : ''}</div>
          <div class="cc-desc">${(c.description || '(No description — open the card to edit)').replace(/</g, '&lt;')}</div>
        </div>
      </div>`).join('');
    grid.querySelectorAll('.char-card').forEach((el) => {
      el.onclick = () => selectChar(el.dataset.name);
      el.ondblclick = () => openEditor(el.dataset.name, false);
    });
  }
  if (list.length && !list.some((c) => c.name === settings.char)) {
    selectChar(list[0].name);
  }
  refreshOrphanHint();
}
// Orphan dirs (chat/media but no card, e.g. legacy main/test) are hidden from
// the grid; surface a one-click cleanup when any exist so the WebUI is never
// stuck listing something it cannot delete.
async function refreshOrphanHint() {
  const el = $('#char-orphans');
  if (!el) return;
  try {
    const { orphans } = await (await api('/api/characters/cleanup')).json();
    const n = (orphans || []).length;
    if (!n) { el.classList.add('hidden'); el.innerHTML = ''; return; }
    el.classList.remove('hidden');
    el.innerHTML = '';
    const span = document.createElement('span');
    span.className = 'meta';
    span.textContent = `Found ${n} legacy directories (chat only, no card): ${orphans.map((o) => o.name).join(', ')}.`;
    const b = document.createElement('button');
    b.textContent = 'Archive & clean';
    b.onclick = async () => {
      if (!(await uiConfirm(`Move these ${n} legacy directories to data/_trash/? (Not deleted; recoverable via shell.)`))) return;
      const r = await api('/api/characters/cleanup', { method: 'POST' });
      const j = await r.json();
      toast(`Archived ${(j.archived || []).length} directories to _trash`);
      refreshChars();
    };
    el.append(span, b);
  } catch { el.classList.add('hidden'); }
}
async function selectChar(name) {
  settings.char = name;
  store.set('settings', settings);
  await api('/api/settings', {
    method: 'PUT', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ current_char: name }),
  });
  document.querySelectorAll('.char-card').forEach((c) => c.classList.toggle('on', c.dataset.name === name));
  syncChatHead();
  loadHistory();
  subscribeChat();
  loadMemoryProfile();
}
function openEditor(name, isNew) {
  editingNew = !!isNew;
  $('#char-editor').classList.remove('hidden');
  $('#char-editor-title').textContent = isNew ? 'New character' : 'Edit character';
  if (isNew) {
    $('#char-name').value = '';
    $('#char-desc').value = '';
    $('#char-avatar').src = EMPTY_AVATAR;
    $('#char-expr').textContent = '';
    $('#char-name').readOnly = false;
    $('#btn-char-delete').style.display = 'none';
  } else {
    api('/api/characters/' + encodeURIComponent(name)).then((r) => r.json()).then((j) => {
      $('#char-name').value = j.name || name;
      $('#char-desc').value = j.description || '';
      $('#char-avatar').src = imgURL(j.avatar_url) || EMPTY_AVATAR;
      $('#char-expr').textContent = (j.expressions || []).length ? 'Expressions: ' + j.expressions.join(', ') : '';
    });
    $('#char-name').readOnly = true;
    $('#btn-char-delete').style.display = 'inline-block';
  }
}
$('#btn-char-new').onclick = () => openEditor(null, true);
$('#btn-char-cancel').onclick = () => $('#char-editor').classList.add('hidden');
$('#btn-char-save').onclick = async () => {
  const name = $('#char-name').value.trim();
  const desc = $('#char-desc').value;
  if (!name) { toast('Name cannot be empty', 'err'); return; }
  if (editingNew) {
    const r = await api('/api/characters', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ name, description: desc }),
    });
    if (!r.ok) {
      const t = (await r.text()).trim();
      if (r.status === 409) { toast('A character with this name already exists — open it to edit, or pick another name', 'err'); return; }
      toast('Create failed (' + r.status + '): ' + (t || 'invalid name?'), 'err');
      return;
    }
    const j = await r.json().catch(() => ({}));
    if (j.reclaimed) toast('Reclaimed the matching legacy directory and wrote the card');
  } else {
    await api('/api/characters/' + encodeURIComponent(name) + '/card', {
      method: 'PUT', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ description: desc }),
    });
  }
  $('#char-editor').classList.add('hidden');
  refreshChars();
  // A brand-new character should become the active one so the user lands on it.
  if (editingNew) selectChar($('#char-name').value.trim());
  else if (name === settings.char) { syncChatHead(); loadMemoryProfile(); }
};
$('#btn-char-delete').onclick = async () => {
  const name = $('#char-name').value.trim();
  if (!name) return;
  const purge = $('#char-purge-chats') && $('#char-purge-chats').checked;
  const msg = purge
    ? `Delete character "${name}" and PERMANENTLY delete its chat, distill records, archive and images? This cannot be undone!`
    : `Delete character "${name}"? Chat data is kept by default (only the card and avatar are removed).`;
  if (!(await uiConfirm(msg))) return;
  await api('/api/characters/' + encodeURIComponent(name) + (purge ? '?chats=1' : ''), { method: 'DELETE' });
  $('#char-editor').classList.add('hidden');
  refreshChars();
};
$('#char-file').onchange = async (e) => {
  const f = e.target.files[0];
  if (!f) return;
  const name = $('#char-name').value.trim();
  if (!name) { toast('Enter a name first', 'err'); e.target.value = ''; return; }
  const fd = new FormData();
  fd.append('file', f);
  const r = await api('/api/characters/' + encodeURIComponent(name) + '/avatar', { method: 'PUT', body: fd });
  const j = await r.json();
  if (j.avatar_url) { $('#char-avatar').src = imgURL(j.avatar_url); syncChatHead(); }
  e.target.value = '';
};

/* ---------- memory: per-character profile ---------- */
// Every character owns a profile (characters/<name>/profile.json): the set of
// collections it RECALLS from (multi-select) and the SINGLE collection the
// distiller WRITES into. Switching characters switches the whole setup, so this
// page always shows the ACTIVE character's values — never a hidden global.
let memAvailable = null; // cached /api/mcp/collections
let memPromptDefault = ''; // built-in distill prompt, used to keep "untouched" = default

async function ensureCollections() {
  if (memAvailable) return memAvailable;
  try {
    const list = await (await api('/api/mcp/collections')).json();
    memAvailable = Array.isArray(list) ? list : [];
  } catch { memAvailable = []; }
  return memAvailable;
}

function collLabel(c) {
  const notes = [];
  if (!c.enabled) notes.push('disabled');
  if (c.exists === false) notes.push('missing');
  const chunks = Number.isFinite(c.chunk_count) ? c.chunk_count.toLocaleString() : '?';
  return `${chunks} chunks${notes.length ? ' · ' + notes.join(', ') : ''}`;
}

function collectionRow({ name, meta, checked, disabled, group }) {
  const row = document.createElement('label');
  row.className = 'coll-item' + (disabled ? ' off' : '');
  const text = document.createElement('span');
  text.className = 'coll-text';
  const n = document.createElement('span');
  n.className = 'coll-name';
  n.textContent = name;
  const m = document.createElement('span');
  m.className = 'coll-meta';
  m.textContent = meta || '';
  text.append(n, m);
  const box = document.createElement('input');
  box.type = 'checkbox';
  box.value = name;
  box.checked = !!checked;
  box.disabled = !!disabled;
  if (group) box.dataset.group = group;
  row.append(text, box);
  return row;
}

function renderRecall(list, want) {
  const wrap = $('#mem-recall');
  if (!wrap) return;
  wrap.innerHTML = '';
  const available = [];
  for (const c of (list || [])) {
    available.push(c.name);
    wrap.appendChild(collectionRow({
      name: c.name, meta: collLabel(c), group: 'recall',
      checked: want.includes(c.name), disabled: !c.enabled || c.exists === false,
    }));
  }
  // A stored id the server did not return stays visible and ticked so a stale
  // selection is never silently dropped (e.g. rag-mcp unreachable).
  for (const id of want) {
    if (!available.includes(id)) {
      wrap.appendChild(collectionRow({ name: id, meta: 'not returned by the server', group: 'recall', checked: true }));
    }
  }
}

function renderWrite(list, value) {
  const sel = $('#mem-write');
  if (!sel) return;
  const names = (list || []).map((c) => c.name);
  if (value && !names.includes(value)) names.unshift(value);
  sel.innerHTML = '<option value="">(none)</option>' + names.map((n) => `<option value="${esc(n)}">${esc(n)}</option>`).join('');
  sel.value = value || '';
}

function renderSections(sections) {
  const wrap = $('#mem-sections');
  if (!wrap) return;
  wrap.textContent = '';
  if (!sections || !sections.length) {
    const s = document.createElement('span');
    s.className = 'meta';
    s.textContent = 'No memory yet — chat a few turns or press Distill now.';
    wrap.appendChild(s);
    return;
  }
  for (const sec of sections) {
    const box = document.createElement('div');
    box.className = 'mem-section';
    const head = document.createElement('div');
    head.className = 'mem-section-head';
    const t = document.createElement('b');
    t.textContent = sec.tag;
    const c = document.createElement('span');
    c.className = 'meta';
    c.textContent = (sec.entries || []).length + ' entries';
    head.append(t, c);
    box.appendChild(head);
    const body = document.createElement('div');
    body.className = 'mem-section-body';
    for (const e of (sec.entries || [])) {
      const line = document.createElement('div');
      line.className = 'mem-line';
      const when = document.createElement('span');
      when.className = 'mem-when';
      when.textContent = e.time ? `${e.date} ${e.time}` : e.date;
      const txt = document.createElement('span');
      txt.className = 'mem-text';
      txt.textContent = e.text;
      line.append(when, txt);
      body.appendChild(line);
    }
    box.appendChild(body);
    wrap.appendChild(box);
  }
}

async function loadMemoryProfile() {
  const char = settings.char;
  const title = $('#mem-char-title');
  if (title) title.textContent = 'Current profile: ' + char;
  try {
    const [prof, dist] = await Promise.all([
      (await api('/api/characters/' + encodeURIComponent(char))).json(),
      (await api('/api/distilled?session=' + encodeURIComponent(char))).json(),
    ]);
    const list = await ensureCollections();
    const recall = prof.recall_collections || [];
    const write = prof.write_collection || '';
    renderRecall(list, recall);
    renderWrite(list, write);
    const badges = $('#mem-char-badges');
    if (badges) {
      badges.textContent = '';
      const b = (txt, cls) => {
        const s = document.createElement('span');
        s.className = 'membadge ' + (cls || '');
        s.textContent = txt;
        badges.appendChild(s);
      };
      b('Recall ' + (recall.length ? '[' + recall.join(', ') + ']' : '(no recall)'), recall.length ? 'on' : 'off');
      b('Write ' + (write || '(none)'), write ? 'on' : 'off');
      if (prof.profile_source === 'migrated') b('Migrated from global', 'warn');
    }
    const warn = $('#mem-char-warn');
    if (warn) {
      if (prof.profile_source === 'migrated') {
        warn.classList.remove('hidden');
        warn.textContent = 'This character has no profile yet; these values were migrated from the old global settings. Save once to persist.';
      } else { warn.classList.add('hidden'); }
    }
    if ($('#mem-distill-enabled')) $('#mem-distill-enabled').checked = !!dist.enabled;
    if ($('#mem-distill-interval')) $('#mem-distill-interval').value = dist.interval || 8;
    // The prompt box always shows the prompt that will actually be sent: the
    // character's own prompt when set, otherwise the built-in default. There is
    // no hidden "default" state any more — clearing the box means "use default".
    if ($('#mem-distill-prompt')) {
      $('#mem-distill-prompt').value = dist.prompt || dist.default_prompt || '';
    }
    memPromptDefault = dist.default_prompt || '';
    if ($('#mem-distill-model')) {
      const want = dist.model || '';
      const sel = $('#mem-distill-model');
      if (!sel.options.length || !Array.from(sel.options).some((o) => o.value === want)) {
        sel.appendChild(new Option(want || '(main chat model)', want));
      }
      sel.value = want;
    }
    if ($('#mem-distill-tokens')) $('#mem-distill-tokens').value = dist.max_tokens || 8192;
    if ($('#mem-distill-meta')) {
      const state = dist.enabled
        ? (dist.need_turns > 0 ? `~${dist.need_turns} more user turns until auto-distill` : 'Ready')
        : 'Auto-distill off';
      $('#mem-distill-meta').textContent = state + ` · ${dist.days || 0} days recorded`;
    }
    if ($('#mem-maxchars')) $('#mem-maxchars').value = dist.max_chars || 5000;
    if ($('#dmem-vec-enabled')) $('#dmem-vec-enabled').checked = !!dist.vectorize_enabled;
    renderSections(dist.sections || []);
  } catch (e) { toast('Failed to load profile: ' + e.message, 'err'); }
}

function selectedRecall() {
  const wrap = $('#mem-recall');
  if (!wrap) return [];
  return Array.from(wrap.querySelectorAll('input[type=checkbox]:checked')).map((b) => b.value);
}

async function saveMemoryGlobal() {
  const r = await api('/api/settings', {
    method: 'PUT', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      mcp_url: $('#mem-url').value.trim(),
      mcp_timeout: +$('#mem-timeout').value || 180,
      mcp_enabled: $('#mem-enabled').checked,
      distill_vectorize_enabled: $('#dmem-vec-enabled') ? $('#dmem-vec-enabled').checked : false,
      distill_max_chars: $('#mem-maxchars') ? (+$('#mem-maxchars').value || 0) : 0,
    }),
  });
  if (!r.ok) throw new Error('HTTP ' + r.status);
  toast('Global connection settings saved');
}

async function saveMemoryProfile() {
  // The prompt box always shows the effective prompt (custom, or the built-in
  // default when the character has none). Sending the default text verbatim
  // back as "custom" would freeze it at today's default, so an untouched box
  // still saves as empty = "use the built-in default".
  const promptEl = $('#mem-distill-prompt');
  const promptVal = promptEl ? promptEl.value : '';
  const body = {
    collections: selectedRecall(),
    write_collection: $('#mem-write') ? $('#mem-write').value : '',
    enabled: $('#mem-distill-enabled') ? $('#mem-distill-enabled').checked : true,
    interval: $('#mem-distill-interval') ? (+$('#mem-distill-interval').value || 8) : 8,
    prompt: promptVal === memPromptDefault ? '' : promptVal,
  };
  const r = await api('/api/characters/' + encodeURIComponent(settings.char) + '/profile', {
    method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body),
  });
  if (!r.ok) throw new Error('HTTP ' + r.status);
  if ($('#mem-profile-saved')) $('#mem-profile-saved').textContent = 'Saved ' + new Date().toLocaleTimeString();
  toast(`Saved the memory profile for "${settings.char}"`);
  await loadMemoryProfile();
}
async function loadServerSettings() {
  try {
    const s = await (await api('/api/settings')).json();
    if (s.upstream) $('#set-upstream').value = s.upstream;
    if (s.rerank_url) $('#set-rerank').value = s.rerank_url;
    if (s.context_window) settings.context_window = s.context_window;
    if (s.reply_reserve) settings.reply_reserve = s.reply_reserve;
    if (s.history_min_turns) settings.history_min_turns = s.history_min_turns;
    store.set('settings', settings);
    $('#set-key-state').textContent = s.api_key_set ? ('API Key set ' + (s.api_key_hint || '')) : 'API Key not set';
    $('#mem-url').value = s.mcp_url || '';
    $('#mem-timeout').value = s.mcp_timeout ?? 180;
    $('#mem-enabled').checked = !!s.mcp_enabled;
    if (s.current_char && s.current_char !== settings.char) { settings.char = s.current_char; store.set('settings', settings); }
    // Seed the local model from the server-shared one when this browser has
    // never picked one, so WebUI and App talk about the same alias.
    if (s.chat_model && !settings.model) { settings.model = s.chat_model; store.set('settings', settings); }
    if (s.user_name) $('#set-user').value = s.user_name;
    $('#set-ntfy-url').value = s.ntfy_url || '';
    $('#set-ntfy-topic').value = s.ntfy_topic || '';
    return s;
  } catch { return null; }
}
$('#btn-mem-save') && ($('#btn-mem-save').onclick = () => asyncAction($('#btn-mem-save'), () => saveMemoryGlobal()));
$('#btn-mem-profile-save') && ($('#btn-mem-profile-save').onclick = () => asyncAction($('#btn-mem-profile-save'), () => saveMemoryProfile()));
// Module settings that live in server settings.json (not the character
// profile): persist on change so they behave like the other autosaved knobs.
async function saveDistillModuleSetting(patch, label) {
  const r = await api('/api/settings', {
    method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(patch),
  });
  if (!r.ok) throw new Error('HTTP ' + r.status);
  toast(label + ' saved');
}
['mem-distill-model', 'mem-distill-tokens', 'mem-maxchars'].forEach((id) => {
  const el = document.getElementById(id);
  if (el) el.addEventListener('change', () => {
    const patch = {};
    let label = '';
    if (id === 'mem-distill-model') { patch.distill_model = el.value.trim(); label = 'Distill model'; }
    if (id === 'mem-distill-tokens') { patch.distill_max_tokens = +el.value || 0; label = 'Distill reply tokens'; }
    if (id === 'mem-maxchars') { patch.distill_max_chars = +el.value || 0; label = 'Fact sheet char cap'; }
    saveDistillModuleSetting(patch, label).catch((e) => toast('Save failed: ' + e.message, 'err'));
  });
});
$('#btn-mem-distill-run') && ($('#btn-mem-distill-run').onclick = () => asyncAction($('#btn-mem-distill-run'), async () => {
  if ($('#mem-distill-meta')) $('#mem-distill-meta').textContent = 'Distilling… (may take a while, keep this page open)';
  const r = await api('/api/distilled/run', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ session: settings.char }) });
  const j = await r.json();
  if (!j.ok) throw new Error(j.error || ('HTTP ' + r.status));
  toast('Distilling done');
  await loadMemoryProfile();
}));
$('#btn-mem-rebuild') && ($('#btn-mem-rebuild').onclick = () => asyncAction($('#btn-mem-rebuild'), async () => {
  if (!(await uiConfirm('Rebuild the fact table from the distilled_memory rows in chat.jsonl? A distilled.md.bak backup is written first; no LLM call.'))) return;
  const r = await api('/api/distilled/rebuild', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ session: settings.char }) });
  const j = await r.json();
  if (!j.ok) throw new Error(j.error || ('HTTP ' + r.status));
  toast(`Backfill done: ${j.sheets} records → ${j.days} days`);
  await loadMemoryProfile();
}));

/* ---------- memory test search: card view ---------- */
// The cards render exactly the chunks that would be injected into the prompt.
// The body is set via textContent, so nothing is escaped, re-flowed or cut.
function memoryHitCard(h) {
  const card = document.createElement('div');
  card.className = 'mem-hit';

  const head = document.createElement('div');
  head.className = 'mem-hit-head';

  const score = document.createElement('span');
  score.className = 'mem-score';
  score.textContent = Number(h.score || 0).toFixed(3);
  head.appendChild(score);

  if (h.source) {
    const src = document.createElement('span');
    src.className = 'mem-src';
    src.textContent = h.source;
    head.appendChild(src);
  }
  if (h.collection) {
    const col = document.createElement('span');
    col.className = 'mem-col';
    col.textContent = h.backend ? h.collection + ' [' + h.backend + ']' : h.collection;
    head.appendChild(col);
  }
  if (h.tags && h.tags.length) {
    const tags = document.createElement('span');
    tags.className = 'mem-tags';
    tags.textContent = h.tags.join(', ');
    head.appendChild(tags);
  }
  card.appendChild(head);

  const ids = [h.id && 'id ' + h.id, h.hash && 'hash ' + h.hash].filter(Boolean).join(' · ');
  if (ids) {
    const idEl = document.createElement('div');
    idEl.className = 'mem-ids';
    idEl.textContent = ids;
    card.appendChild(idEl);
  }

  const body = document.createElement('div');
  body.className = 'mem-hit-body';
  body.textContent = h.text || '';
  card.appendChild(body);
  return card;
}

function renderMemoryHits(j) {
  const out = $('#mem-test-out');
  out.textContent = '';
  const hits = Array.isArray(j.hits) ? j.hits : [];

  const meta = document.createElement('div');
  meta.className = 'mem-hits-meta meta';
  meta.textContent = (j.chars || 0) + ' chars · ' + hits.length + ' hit(s) · ' + (j.collection || '(server default)');
  out.appendChild(meta);

  if (!hits.length) {
    const raw = document.createElement('pre');
    raw.className = 'mem-raw';
    raw.textContent = j.text || '(no results)';
    out.appendChild(raw);
    return;
  }
  for (const h of hits) out.appendChild(memoryHitCard(h));
}

$('#btn-mem-test').onclick = async () => {
  const q = $('#mem-test-q').value.trim() || 'test';
  $('#mem-test-out').textContent = 'Searching…';
  try {
    const j = await (await api('/api/memory/search', {
      method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ query: q, session: settings.char }),
    })).json();
    if (!j.ok) { $('#mem-test-out').textContent = 'Failed: ' + (j.error || 'search failed'); return; }
    renderMemoryHits(j);
  } catch (e) { $('#mem-test-out').textContent = 'Failed: ' + e; }
};
async function saveNtfy(silent) {
  await api('/api/settings', {
    method: 'PUT', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ ntfy_url: $('#set-ntfy-url').value.trim(), ntfy_topic: $('#set-ntfy-topic').value.trim() }),
  });
  if (!silent) toast('Push settings saved');
}
$('#btn-ntfy-save').onclick = () => asyncAction($('#btn-ntfy-save'), () => saveNtfy(false));
['set-ntfy-url', 'set-ntfy-topic'].forEach((id) => {
  const el = document.getElementById(id); if (el) el.addEventListener('change', () => saveNtfy(true).catch((e) => toast('Push save failed: ' + e.message, 'err')));
});

/* ---------- distilled memory ---------- */
// The distilled-memory UI (prompt, interval, enable, section boxes) and its
// handlers live in the per-character profile section above (loadMemoryProfile /
// saveMemoryProfile), because distill settings are now part of the character's
// profile rather than a global setting.

/* ---------- models ---------- */
// The main chat model is shared with the Flutter app: it lives server-side as
// settings.chat_model. The browser keeps its own copy in localStorage purely
// so the dropdown survives an offline reload; picking one writes both.
function setChatModel(model, persist) {
  settings.model = model;
  store.set('settings', settings);
  if (persist) {
    api('/api/settings', {
      method: 'PUT', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ chat_model: model }),
    }).catch(() => {});
  }
}

async function refreshModels() {
  try {
    const j = await (await api('/api/models')).json();
    const ids = (j.data || []).map((m) => m.id).filter(Boolean);
    const sel = $('#set-model');
    sel.innerHTML = ids.map((id) => `<option value="${id}">${id}</option>`).join('');
    if (ids.includes(settings.model)) sel.value = settings.model;
    else if (ids.length) { setChatModel(ids[0], true); sel.value = ids[0]; }
    // Same alias list feeds the dedicated distill-model picker. Keep the
    // current value even if one-api no longer offers it, so a temporarily
    // disabled alias stays visible instead of silently resetting.
    const dsel = $('#mem-distill-model');
    if (dsel) {
      const cur = dsel.value;
      dsel.innerHTML = '<option value="">(main chat model)</option>' +
        ids.map((id) => `<option value="${id}">${id}</option>`).join('');
      if (cur) {
        if (!ids.includes(cur)) dsel.appendChild(new Option(cur + ' (not offered)', cur));
        dsel.value = cur;
      }
    }
  } catch { /* silent while upstream is unreachable */ }
  checkModelStatus();
}

// Warn (sticky banner) when the configured main model is no longer offered by
// one-api — the exact silent-403 failure mode. Purely advisory.
async function checkModelStatus() {
  const el = $('#model-banner');
  if (!el) return;
  try {
    const st = await (await api('/api/model-status')).json();
    if (st && st.ok === false) {
      const avail = (st.available || []).slice(0, 8).join(', ');
      el.innerHTML = `⚠ Main model "${st.model || '?'}" unavailable: ${st.error || 'not in the upstream model list'}.` +
        (avail ? ` Available: ${avail}…` : '') +
        ` <a id="model-banner-fix">Settings</a>`;
      el.classList.remove('hidden');
      const fix = $('#model-banner-fix');
      if (fix) fix.onclick = () => { document.querySelector('[data-page="settings"]')?.click(); };
    } else {
      el.classList.add('hidden');
    }
  } catch { el.classList.add('hidden'); }
}
$('#btn-models-refresh').onclick = refreshModels;
$('#set-model').onchange = (e) => setChatModel(e.target.value, true);

/* ---------- settings ---------- */
function fillSettingsForm() {
  $('#set-window').value = settings.context_window || 16384;
  $('#set-reserve').value = settings.reply_reserve || 4096;
  $('#set-uiscale').value = settings.ui_scale || 100;
  $('#set-minrounds').value = settings.history_min_turns || 4;
  $('#set-rerank').value = settings.rerank_url;
  $('#set-stream').checked = !!settings.stream;
  $('#set-visible').value = settings.visible_turns || 10;
  $('#set-user').value = settings.user_name || '';
  $('#set-avatar-size').value = settings.avatar_px || 88;
  applyTheme();
}
async function saveSettings(silent) {
  settings.context_window = Math.min(131072, Math.max(2048, +$('#set-window').value || 16384));
  settings.reply_reserve = Math.min(65536, Math.max(256, +$('#set-reserve').value || 4096));
  settings.ui_scale = Math.min(150, Math.max(80, +$('#set-uiscale').value || 100));
  settings.history_min_turns = Math.min(50, Math.max(1, +$('#set-minrounds').value || 4));
  settings.rerank_url = $('#set-rerank').value.trim() || settings.rerank_url;
  settings.stream = $('#set-stream').checked;
  settings.visible_turns = Math.min(200, Math.max(5, +$('#set-visible').value || 10));
  settings.avatar_px = Math.min(240, Math.max(32, +$('#set-avatar-size').value || 88));
  settings.user_name = $('#set-user').value.trim() || 'user';
  settings.model = $('#set-model').value || settings.model;
  store.set('settings', settings);
  applyUiScale();
  // Only persist upstream/key when the user actually edited them. The browser's
  // password manager otherwise autofills this form with the saved login
  // username/password, which would overwrite the real One-API config.
  const body = {
    rerank_url: $('#set-rerank').value.trim(),
    user_name: settings.user_name,
    context_window: settings.context_window,
    reply_reserve: settings.reply_reserve,
    history_min_turns: settings.history_min_turns,
    chat_model: settings.model || '',
  };
  if (upstreamDirty) body.upstream = $('#set-upstream').value.trim();
  if (apiKeyDirty && $('#set-apikey').value) body.api_key = $('#set-apikey').value;
  const r = await api('/api/settings', {
    method: 'PUT', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  if (!r.ok) throw new Error('HTTP ' + r.status);
  upstreamDirty = false;
  apiKeyDirty = false;
  $('#set-apikey').value = '';
  applyAvatarSize();
  refreshUserAvatarGutters(); // user_name changed: refresh initial-avatars too
  applyTheme();
  if (!silent) toast('Preferences saved');
  return r;
}
$('#btn-settings-save').onclick = () => asyncAction($('#btn-settings-save'), async () => { await saveSettings(false); await loadServerSettings(); refreshModels(); });
/* ---------- user avatar (global, settings page) ---------- */
{ const f = $('#user-avatar-file');
  if (f) f.addEventListener('change', async (e) => {
    const file = e.target.files[0];
    e.target.value = '';
    if (!file) return;
    if (!file.type.startsWith('image/')) { toast('Selected file is not an image', 'err'); return; }
    if (file.size > 8 * 1024 * 1024) { toast('Image too large (max 8MB)', 'err', 4200); return; }
    const fd = new FormData();
    fd.append('file', file);
    try {
      const r = await api('/api/user/avatar/', { method: 'PUT', body: fd });
      const j = await r.json().catch(() => ({}));
      if (!r.ok) throw new Error((j && j.error) || ('HTTP ' + r.status));
      userAvatarURL = j.avatar_url || '';
      refreshUserAvatarGutters();
      toast('My avatar updated');
    } catch (err) { toast('Upload failed: ' + (err.message || err), 'err', 4200); }
  });
}
{ const del = $('#btn-user-avatar-del');
  if (del) del.onclick = () => asyncAction(del, async () => {
    await api('/api/user/avatar/', { method: 'DELETE' });
    userAvatarURL = '';
    refreshUserAvatarGutters();
    toast('My avatar removed; using name initial');
  });
}
// Theme picker: local-only, applies instantly (no server round-trip).
{ const t = $('#set-theme'); if (t) t.addEventListener('change', () => setTheme(t.value)); }
document.querySelectorAll('[data-themek]').forEach((el) => {
  el.addEventListener('input', () => setThemeCustom(el.dataset.themek, el.value));
});
// autosave: any change in the settings form persists (debounced)
let settingsSaveTimer = null;
function scheduleSettingsSave() {
  clearTimeout(settingsSaveTimer);
  settingsSaveTimer = setTimeout(() => saveSettings(true).catch((e) => toast('Preference save failed: ' + e.message, 'err')), 600);
}
['set-window', 'set-reserve', 'set-uiscale', 'set-minrounds', 'set-rerank', 'set-stream', 'set-visible', 'set-avatar-size', 'set-user']
  .forEach((id) => { const el = document.getElementById(id); if (el) el.addEventListener('change', scheduleSettingsSave); });
// upstream/key are saved only via the explicit button (dirty-tracked).
let upstreamDirty = false, apiKeyDirty = false;
{ const u = $('#set-upstream'); if (u) u.addEventListener('input', () => { upstreamDirty = true; }); }
{ const k = $('#set-apikey'); if (k) k.addEventListener('input', () => { apiKeyDirty = true; }); }

/* ---------- cross-device refresh on tab focus ---------- */
document.addEventListener('visibilitychange', () => {
  if (!document.hidden) { subscribeChat(); }
});

/* ---------- init ---------- */
const _loginForm = $('#login-form');
if (_loginForm) _loginForm.addEventListener('submit', (e) => { e.preventDefault(); doLogin(); });
if ($('#btn-logout')) $('#btn-logout').onclick = doLogout;
async function init() {
  applyTheme(); // before auth gate so the login screen already wears it
  applyNavCollapsed();
  if (!(await bootAuth())) return; // gate: stop booting until logged in
  applyTheme();
  applyNavCollapsed();
  fillSettingsForm();
  applyUiScale();
  await loadServerSettings();
  fillSettingsForm();
  applyUiScale();
  $('#chat-char-name').textContent = settings.char;
  applyAvatarSize();
  loadUserAvatar();
  loadModules();
  refreshChars();
  refreshModels();
  syncChatHead();
  loadHistory();
  subscribeChat();
  loadMemoryProfile();
}
init();
