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

let blocks = [];
const settings = Object.assign(
  {
    context_window: 16384, reply_reserve: 4096, history_min_turns: 4,
    rerank_url: 'http://127.0.0.1:11437', char: 'Leer乐儿', model: '',
    stream: false, visible_turns: 10, user_name: 'RoyChong', avatar_px: 88,
    ui_scale: 100, theme: 'st-dark', theme_custom: {},
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
    showLogin('登录已过期，请重新登录');
    throw new Error('HTTP 401 未登录');
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
    $('#login-err').textContent = '登录失败：' + (e.message || e);
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
  if (!el || typeof window.hljs === 'undefined' || !window.hljs.highlightElement) return;
  try {
    el.querySelectorAll('pre code').forEach((c) => {
      if (c.dataset.hljs) return;
      c.dataset.hljs = '1';
      window.hljs.highlightElement(c);
    });
  } catch (e) {
    console.warn('[TavernLab] hljs error:', e);
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
  renderMath(el);
  highlightCode(el);
  if (el.innerHTML.indexOf(MONEY_PH) >= 0) {
    el.innerHTML = el.innerHTML.split(MONEY_PH).join('$');
  }
}

/* ---------- theme (custom colors; local-only preference) ---------- */
const THEMES = {
  'st-dark': { name: 'ST 暗灰（默认）' },
  'tavern-glass': { name: 'Tavern 玻璃（旧版）' },
  'paper': { name: '纸白（浅色）' },
  'custom': { name: '自定义' },
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
  u.title = settings.user_name || '你';
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
  const n = (settings.user_name || '你').trim();
  return (n ? [...n][0] : '你').toUpperCase();
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
  const d = document.createElement('div');
  d.className = 'toast ' + kind;
  d.textContent = msg;
  w.appendChild(d);
  setTimeout(() => { d.style.opacity = '0'; d.style.transition = 'opacity .2s'; setTimeout(() => d.remove(), 220); }, ms);
}
async function asyncAction(btn, fn) {
  if (btn) { btn.disabled = true; btn.classList.add('loading'); }
  try {
    const r = await fn();
    return r;
  } catch (e) {
    toast('失败：' + (e && e.message ? e.message : e), 'err', 4200);
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
    const frames = ['努力回复中', '努力回复中·', '努力回复中··', '努力回复中···'];
    let i = 0;
    el.classList.add('pending');
    el.classList.remove('pending-dots');
    el.textContent = frames[0];
    pendingTimer = setInterval(() => { i = (i + 1) % frames.length; el.textContent = frames[i]; }, 420);
  } else {
    el.classList.remove('pending');
    // mood text is restored by classifyAndBadge; fall back if it never resolves
    if (el.textContent.startsWith('努力回复中')) el.textContent = '心情 · —';
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
    if (b.dataset.page === 'audit') refreshAudit();
    if (b.dataset.page === 'chars') refreshChars();
    if (b.dataset.page === 'logs') openLogs(); else stopLogStream();
    if (b.dataset.page === 'gps') refreshGPS();
  };
});
$('#hamburger').onclick = () => document.body.classList.toggle('nav-open');

/* ---------- health ---------- */
fetch('/api/health').then((r) => r.json()).then((h) => {
  const el = $('#health');
  el.textContent = '● ' + h.upstream;
  el.classList.add('ok');
}).catch(() => { $('#health').textContent = '● 后端未连接'; });

/* ---------- blocks ---------- */
async function loadBlocks() {
  blocks = await (await api('/api/blocks')).json();
  if (!Array.isArray(blocks)) blocks = [];
  renderBlocks();
}
let blockSaveTimer = null;
function scheduleBlockSave() {
  setUnsaved(true);
  clearTimeout(blockSaveTimer);
  blockSaveTimer = setTimeout(() => saveBlocks(), 600);
}
function setUnsaved(on) {
  const b = $('#btn-save-blocks');
  if (b) b.textContent = on ? '保存 •（未保存）' : '保存';
}
async function saveBlocks(quiet) {
  clearTimeout(blockSaveTimer);
  try {
    const r = await api('/api/blocks', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(blocks) });
    if (!r.ok) throw new Error('HTTP ' + r.status);
    setUnsaved(false);
    if (!quiet) toast('Blocks 已保存');
  } catch (e) {
    toast('Blocks 保存失败：' + e.message, 'err', 4200);
    setUnsaved(true);
  }
}
let dragId = null;
const LEVELS = [
  { v: 1, t: 'L1 锁定（永不裁剪）' },
  { v: 2, t: 'L2 可裁剪（保底后淘汰）' },
  { v: 3, t: 'L3 弹性（最先整个砍）' },
];
const SOURCES = ['static', 'character', 'distilled_state', 'distilled_log', 'distilled', 'mcp', 'chat'];
function renderBlocks() {
  const el = $('#blocks');
  el.innerHTML = '';
  [...blocks].sort((a, b) => a.order - b.order).forEach((b) => {
    const d = document.createElement('div');
    d.className = 'block';
    d.draggable = true;
    d.dataset.id = b.id;
    const lvl = b.level || 3;
    const src = (b.source && b.source.type) || 'static';
    const levelOpts = LEVELS.map((x) => `<option value="${x.v}" ${x.v === lvl ? 'selected' : ''}>${x.t}</option>`).join('');
    const srcOpts = SOURCES.map((s) => `<option value="${s}" ${s === src ? 'selected' : ''}>${s}</option>`).join('');
    const roleOpts = ['system', 'user', 'assistant'].map((r) => `<option value="${r}" ${r === b.role ? 'selected' : ''}>${r}</option>`).join('');
    d.innerHTML = `
      <div class="hd">
        <label><span class="grip" title="只从这里拖动排序">⠿</span><input type="checkbox" data-id="${b.id}" data-k="enabled" ${b.enabled ? 'checked' : ''}> <b>${b.id}</b></label>
        <span class="blk-actions">
          <select data-id="${b.id}" data-k="level" title="级别决定超预算时的淘汰顺序">${levelOpts}</select>
          <button class="danger" data-del="${b.id}" title="删除此 block">✕</button>
        </span>
      </div>
      <div class="meta blk-meta">
        role <select data-id="${b.id}" data-k="role">${roleOpts}</select>
        source <select data-id="${b.id}" data-k="source">${srcOpts}</select>
        <label title="L2 淘汰优先级：数字小=先被砍；留空=按来源默认(chat 0 / RAG 1 / 日记 2)">priority
          <input type="number" data-id="${b.id}" data-k="evict_priority" value="${b.evict_priority ?? ''}" placeholder="auto" style="width:64px"></label>
        ${b.source && b.source.collection ? '<span>collection: ' + b.source.collection + '</span>' : ''}
      </div>
      <textarea rows="2" data-id="${b.id}" data-k="template" placeholder="模板；list 源用 {{rag}} / {{items}} 占位">${(b.template || '').replace(/</g, '&lt;')}</textarea>`;
    el.appendChild(d);
  });
  el.querySelectorAll('input,textarea,select').forEach((inp) => {
    inp.onchange = () => {
      const b = blocks.find((x) => x.id === inp.dataset.id);
      if (!b) return;
      const k = inp.dataset.k;
      if (k === 'enabled') b.enabled = inp.checked;
      else if (k === 'level') b.level = +inp.value;
      else if (k === 'role') b.role = inp.value;
      else if (k === 'evict_priority') {
        const v = inp.value.trim();
        if (v === '') delete b.evict_priority; else b.evict_priority = +v;
      } else if (k === 'source') b.source = Object.assign({}, b.source, { type: inp.value });
      else b[k] = inp.value;
      scheduleBlockSave();
    };
  });
  el.querySelectorAll('[data-del]').forEach((btn) => {
    btn.onclick = () => {
      const id = btn.dataset.del;
      if (!confirm(`删除 block「${id}」？`)) return;
      blocks = blocks.filter((x) => x.id !== id);
      renderBlocks();
      scheduleBlockSave();
    };
  });
  // vertical-only drag from the grip; drop before/after by pointer half.
  el.querySelectorAll('.block').forEach((d) => {
    d.addEventListener('dragstart', (e) => {
      if (!e.target.closest('.grip')) { e.preventDefault(); return; }
      dragId = d.dataset.id;
      d.classList.add('dragging');
      e.dataTransfer.effectAllowed = 'move';
    });
    d.addEventListener('dragend', () => { d.classList.remove('dragging'); dragId = null; clearDropMarks(el); });
    d.addEventListener('dragover', (e) => {
      if (!dragId || d.dataset.id === dragId) return;
      e.preventDefault();
      e.dataTransfer.dropEffect = 'move';
      const r = d.getBoundingClientRect();
      const after = e.clientY > r.top + r.height / 2;
      clearDropMarks(el);
      d.classList.add(after ? 'drop-after' : 'drop-before');
      d.dataset.after = after ? '1' : '0';
    });
    d.addEventListener('dragleave', () => d.classList.remove('drop-before', 'drop-after'));
    d.addEventListener('drop', (e) => {
      e.preventDefault();
      const after = d.dataset.after === '1';
      clearDropMarks(el);
      if (dragId && d.dataset.id !== dragId) reorderBlocks(dragId, d.dataset.id, after);
      dragId = null;
    });
  });
}
function clearDropMarks(el) {
  el.querySelectorAll('.block').forEach((x) => x.classList.remove('drop-before', 'drop-after'));
}
function reorderBlocks(fromId, toId, after) {
  const ordered = [...blocks].sort((a, b) => a.order - b.order);
  const from = ordered.findIndex((b) => b.id === fromId);
  const to = ordered.findIndex((b) => b.id === toId);
  if (from < 0 || to < 0) return;
  const [mv] = ordered.splice(from, 1);
  let idx = ordered.findIndex((b) => b.id === toId);
  if (after) idx++;
  ordered.splice(idx, 0, mv);
  ordered.forEach((b, n) => { b.order = n; });
  blocks = ordered;
  renderBlocks();
  scheduleBlockSave();
}
function ctxCfg() {
  return {
    context_window: settings.context_window,
    reply_reserve: settings.reply_reserve,
    history_min_turns: settings.history_min_turns,
  };
}
function budgetLine(res) {
  const drop = (res.dropped || []).join(', ') || '无';
  const blocksTxt = (res.blocks || []).map((b) => `${b.id}(L${b.level}):${b.tokens}${b.truncated ? '✂' : ''}`).join(' · ');
  const overflow = res.overflow ? ' · <span style="color:#ffb4b4">⚠ OVERFLOW（超过输入预算）</span>' : '';
  const win = res.window ? `窗口 ${Math.round(res.window / 1024)}k · ` : '';
  return `<b>${res.total_tokens}</b> / ${res.budget_tokens} 输入 tokens · ${win}dropped: ${drop}${overflow}<br>${blocksTxt}`;
}
async function assemble() {
  const r = await api('/api/assemble', {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ blocks, context: ctxCfg(), session: settings.char }),
  });
  const res = await r.json();
  $('#budget').innerHTML = budgetLine(res);
  return res;
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
document.addEventListener('DOMContentLoaded', () => {
  const box = chatBox();
  if (box) {
    box.addEventListener('scroll', () => {
      userPinned = isNearBottom(box);
      const b = $('#btn-bottom');
      if (b) b.classList.toggle('hidden', userPinned);
    });
  }
  const bb = $('#btn-bottom');
  if (bb) bb.onclick = () => { const b2 = chatBox(); if (b2) { scrollToBottom(b2); userPinned = true; bb.classList.add('hidden'); } };
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
  const defaultWho = role === 'user' ? (settings.user_name || '你') : role === 'assistant' ? settings.char : (role === 'distilled_memory' ? 'Distilled Memory' : '系统');
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
async function loadHistory() {
  historyShown = 0;
  seenMsgIds.clear();
  $('#chat').innerHTML = '';
  await loadEarlier();
  const box = chatBox();
  if (box) { scrollToBottom(box); userPinned = true; }
}
async function loadEarlier() {
  const q = new URLSearchParams({ session: settings.char, limit: settings.visible_turns, before: historyShown });
  const j = await (await api('/api/history?' + q)).json();
  const msgs = j.messages || [];
  if (!msgs.length) { $('#btn-earlier').textContent = '没有更早了'; return; }
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
  $('#btn-earlier').textContent = j.has_more ? '↑ 加载更早' : '没有更早了';
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
    $('#expr-badge').textContent = j.fallback ? '心情 · 😐 平静' : '心情 · 😊 ' + j.label;
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
  } catch { /* 静默：表情失败不打断聊天 */ }
}
/* ---------- image attach ---------- */
let pendingImages = [];
const MAX_IMG_MB = 8;
function addPendingImage(dataUrl, name) {
  if (!dataUrl || !dataUrl.startsWith('data:image/')) {
    toast('不支持的图片格式' + (name ? '：' + name : ''), 'err');
    return false;
  }
  // ~4/3 overhead for base64; reject loudly instead of silent drop (P0).
  const mb = (dataUrl.length * 3 / 4) / 1024 / 1024;
  if (mb > MAX_IMG_MB) {
    toast(`图片太大（约${mb.toFixed(1)}MB，上限${MAX_IMG_MB}MB），请压缩后重发`, 'err', 4200);
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
    chip.innerHTML = `<img src="${d}"><button title="移除">✕</button>`;
    chip.querySelector('button').onclick = () => { pendingImages.splice(i, 1); renderImgPreview(); };
    box.appendChild(chip);
  });
}
$('#btn-attach').onclick = () => $('#img-file').click();
$('#img-file').onchange = async (e) => {
  const files = [...(e.target.files || [])];
  // Single image only: most vision APIs can't handle multiple. New pick replaces.
  const f = files.find((x) => x.type.startsWith('image/'));
  if (!f) { if (files.length) toast('选中的不是图片', 'err'); }
  else if (f.size > MAX_IMG_MB * 1024 * 1024) {
    toast(`图片太大（${(f.size / 1048576).toFixed(1)}MB，上限${MAX_IMG_MB}MB）`, 'err', 4200);
  } else if (f) {
    try {
      const d = await new Promise((res, rej) => { const r = new FileReader(); r.onload = () => res(r.result); r.onerror = rej; r.readAsDataURL(f); });
      addPendingImage(d, f.name);
    } catch { toast('图片读取失败', 'err'); }
  }
  renderImgPreview();
  e.target.value = '';
};

// Slash-commands understood by the composer. Only exact matches are commands:
// any other text beginning with "/" is sent to the model as a normal message.
const REGEN_COMMANDS = new Set(['/regenerate', '/regen', '/重发', '/重新生成', '/重新回复']);
function isRegenCommand(t) { return REGEN_COMMANDS.has((t || '').trim().toLowerCase()); }

async function send(regen = false) {
  const ta = $('#input');
  const text = ta.value.trim();
  // /regenerate: re-run the last user turn. The server drops the trailing
  // assistant reply first, so the partial leaves the context and a fresh
  // reply is generated. No new user bubble is added.
  if (!regen && isRegenCommand(text)) { await send(true); return; }
  const imgs = regen ? [] : pendingImages.slice();
  if (!regen && !text && !imgs.length) return;
  if (regen) {
    const ais = [...document.querySelectorAll('#chat .msg.ai')];
    const last = ais.pop();
    if (last) last.remove(); // replaced by the streamed reply
  } else {
    addMsg('user', text || '(图片)', null, imgs);
  }
  ta.value = '';
  if (!regen) { pendingImages = []; renderImgPreview(); }
  setPending(true);
  userPinned = true;
  const cbox = chatBox();
  if (cbox) scrollToBottom(cbox);
  const stream = !!settings.stream;
  const seqAtStart = assistantSseSeq;
  const maxTokens = Math.min(65536, Math.max(256, +settings.reply_reserve || 4096));
  const body = regen
    ? { model: settings.model || undefined, session: settings.char, regenerate: true, stream, blocks, context: ctxCfg(), max_tokens: maxTokens }
    : { model: settings.model || undefined, session: settings.char, text, images: imgs, stream, blocks, context: ctxCfg(), max_tokens: maxTokens };
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
            if (delta) { full += delta; belly.textContent = full; belly.dataset.raw = full; }
          } catch (err) { console.warn('sse parse', err, payload); }
        }
      }
      belly.classList.remove('cursor');
      // Stream complete: full rich render (markdown+LaTeX+hljs)
      if (full.trim()) {
        renderRichBody(belly, full, false);
      } else {
        belly.textContent = '（空回复）';
      }
      pendingAssistant = null;
      setPending(false);
      if (streamError) toast('上游流中断，回复可能不完整：' + streamError, 'err', 6000);
      else if (finishReason === 'length') toast('回复触到生成上限被截断（finish=length），可调大“生成上限”', 'err', 5000);
      else if (finishReason === 'content_filter') toast('回复被安全策略截断（finish=content_filter）', 'err', 5000);
      if (full.trim()) classifyAndBadge(full, box);
      return;
    }
    const r = await api('/v1/chat/completions', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
    const j = await r.json().catch(() => ({}));
    if (!r.ok) {
      const detail = (typeof j === 'string' ? j : JSON.stringify(j)).slice(0, 300);
      if (r.status === 400 && /图片/.test(detail)) throw new Error('图片被拒：' + detail);
      throw new Error('HTTP ' + r.status + ' ' + detail);
    }
    const reply = j.choices?.[0]?.message?.content || '（无内容）';
    const finish = j.choices?.[0]?.finish_reason || '';
    if (finish === 'length') toast('回复被长度截断（finish=length），可调大“生成上限”', 'err', 4200);
    else if (finish === 'content_filter') toast('回复被安全策略截断（finish=content_filter）', 'err', 4200);
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
    const box = addMsg('assistant', '⚠ 发送失败：' + (e.message || e), 'err');
    const btn = document.createElement('button');
    btn.className = 'retry';
    btn.textContent = '重试';
    btn.onclick = () => { box.remove(); ta.value = text; pendingImages = imgs; renderImgPreview(); send(); };
    box.querySelector('.body').appendChild(document.createElement('br'));
    box.querySelector('.body').appendChild(btn);
    toast('发送失败：' + (e.message || e), 'err', 4200);
  }
}
$('#btn-send').onclick = () => asyncAction($('#btn-send'), send);
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
    if (f.size > MAX_IMG_MB * 1024 * 1024) { toast('粘贴图片太大，请压缩后重发', 'err'); return; }
    try {
      const d = await new Promise((res, rej) => { const r = new FileReader(); r.onload = () => res(r.result); r.onerror = rej; r.readAsDataURL(f); });
      addPendingImage(d, 'paste');
    } catch { toast('图片读取失败', 'err'); }
  }
  renderImgPreview();
});
$('#btn-assemble').onclick = assemble;
$('#btn-preview').onclick = async () => {
  const res = await assemble();
  if (!res) return;
  const m = res.memory || {};
  $('#preview-memory').textContent = m.enabled
    ? `MCP: ${m.collection} · query=「${(m.query || '').slice(0, 60)}」 · hits=${m.hits ?? '?'} · 预算${m.budget_tokens ?? '?'}用了${m.used_tokens ?? '?'}${m.error ? ' · ⚠ ' + m.error : ''}`
    : 'MCP 未启用：本轮无记忆注入';
  $('#preview-blocks').innerHTML = budgetLine(res);
  $('#preview-text').textContent = res.prompt_text || '(空)';
  $('#preview-modal').classList.remove('hidden');
};
$('#btn-preview-close').onclick = () => $('#preview-modal').classList.add('hidden');
$('#preview-modal').addEventListener('click', (e) => { if (e.target.id === 'preview-modal') e.target.classList.add('hidden'); });
$('#btn-save-blocks').onclick = () => asyncAction($('#btn-save-blocks'), async () => { await saveBlocks(false); assemble(); });
$('#btn-reload-blocks').onclick = () => asyncAction($('#btn-reload-blocks'), async () => { await loadBlocks(); toast('已重载'); });
$('#btn-add-block').onclick = () => {
  const id = (prompt('新 block 的名字（英文/数字，唯一）') || '').trim();
  if (!id) return;
  if (blocks.some((b) => b.id === id)) { toast('名字已存在', 'err'); return; }
  const maxOrder = blocks.reduce((m, b) => Math.max(m, b.order || 0), 0);
  blocks.push({
    id, role: 'system', order: maxOrder + 1, enabled: true, level: 3,
    budget: {}, source: { type: 'static' }, template: '',
  });
  renderBlocks();
  scheduleBlockSave();
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
  if (!confirm(`归档「${settings.char}」当前楼并另起新楼？归档进该角色数据包，聊天数据不会丢。`)) return;
  const j = await (await api('/api/archive', {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ session: settings.char }),
  })).json();
  if (j.ok) { loadHistory(); } else alert('归档失败。');
};

/* ---------- audit ---------- */
async function refreshAudit() {
  const { ids } = await (await api('/api/audit')).json();
  $('#audit-list').innerHTML = ids.map((id) => `<option>${id}</option>`).join('');
  if (ids.length) { $('#audit-list').value = ids[0]; viewAudit(); }
  else { $('#audit').textContent = '暂无记录，先去聊一句。'; $('#audit-summary').textContent = ''; $('#audit-blocks').innerHTML = ''; }
}
function esc(s) { return String(s ?? '').replace(/</g, '&lt;'); }
async function viewAudit() {
  const id = $('#audit-list').value;
  if (!id) return;
  const a = await (await api('/api/audit/' + id)).json();
  const mem = a.memory || {};
  const up = a.upstream_usage || {};
  const est = a.estimate_tokens ?? a.total_tokens;
  const act = a.actual_tokens || up.prompt_tokens || 0;
  const comp = a.completion_tokens || up.completion_tokens || 0;
  const fin = a.finish_reason || '';
  const truncWarn = fin === 'length' ? ' · <span style="color:#ffb4b4">⚠ 生成被截断（finish=length），调大“回复预留”</span>' : '';
  const overWarn = a.overflow ? ' · <span style="color:#ffb4b4">⚠ OVERFLOW（超过输入预算）</span>' : '';
  const memTxt = mem.enabled
    ? `记忆：${esc(mem.collection || '')} · query=「${esc((mem.query || '').slice(0, 60))}」 · hits=${mem.hits ?? '?'} · 预算${mem.budget_tokens ?? '?'}用了${mem.used_tokens ?? '?'}${mem.error ? ' · ⚠ ' + esc(mem.error) : ''}`
    : '记忆：未启用';
  $('#audit-summary').innerHTML =
    `<b>${a.total_tokens}</b> / ${a.budget_tokens} 输入 tokens（估算${est} vs 实际${act || '?'}` +
    `${comp ? ' · 补全' + comp : ''} · max_tokens${a.max_tokens || '?'}${fin ? ' · finish=' + esc(fin) : ''}` +
    `）· 窗口 ${a.window ? Math.round(a.window / 1024) + 'k' : '?'} · dropped: ${esc((a.dropped || []).join(', ') || '无')}` +
    `${truncWarn}${overWarn}<br>${memTxt}` +
    `<br><span class="meta">滑动窗口：输入预算 = 窗口 − 回复预留。L1 永不裁剪；L3 先整个砍；L2 按级别淘汰（RAG 砍最低分，chat 淘汰最旧、保底 4 轮）。</span>`;
  const rows = (a.blocks || []).map((b) =>
    `<tr><td>${esc(b.id)}</td><td>L${b.level || '?'}</td><td>${esc(b.role)}</td><td>${b.tokens}</td><td>${b.truncated ? '✂' : ''}</td><td class="meta">${esc(b.note || '')}</td></tr>`).join('');
  $('#audit-blocks').innerHTML =
    `<table><tr><th>block</th><th>级别</th><th>role</th><th>tokens</th><th>淘汰</th><th>备注</th></tr>${rows}</table>`;
  $('#audit').textContent = JSON.stringify(a, null, 2);
}
$('#btn-audit-list').onclick = refreshAudit;
$('#btn-audit-view').onclick = viewAudit;

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
  $('#log-state').textContent = '实时中…';
  if ($('#log-live').checked) startLogStream();
}
function startLogStream() {
  stopLogStream();
  logES = new EventSource(withToken('/api/logs/stream'));
  logES.addEventListener('log', (ev) => {
    try { renderLogEntries([JSON.parse(ev.data)]); } catch {}
    if ($('#log-autoscroll').checked) { const v = $('#log-view'); v.scrollTop = v.scrollHeight; }
    $('#log-state').textContent = '实时中…';
  });
  logES.onerror = () => { $('#log-state').textContent = '连接中断，重连中…'; };
}
$('#btn-log-refresh').onclick = () => asyncAction($('#btn-log-refresh'), openLogs);
$('#log-live').onchange = () => { if ($('#log-live').checked) startLogStream(); else { stopLogStream(); $('#log-state').textContent = '已暂停'; } };
$('#log-level').onchange = async () => {
  await api('/api/logs', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ level: $('#log-level').value }) });
  openLogs();
};
$('#btn-log-copy').onclick = () => asyncAction($('#btn-log-copy'), async () => {
  await navigator.clipboard.writeText($('#log-view').innerText);
  toast('日志已复制');
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
      $('#gt20-render').textContent = '{{location}} → ' + (loc.render || '(空)') +
        `\n坐标 ${p.lat}, ${p.lon} · acc ${p.acc || '?'}m · batt ${p.batt ?? '?'}% · provider ${p.provider || '?'}` +
        `\nplace: ${g.place || '?'}  [${(g.hierarchy || []).join(' > ')}]`;
    } else { $('#gt20-render').textContent = '暂无定位上报'; }
    const pts = await (await api('/api/location/points?cap=50')).json();
    const rows = (pts.points || []).slice(-20).map(p =>
      `${new Date(p.tst*1000).toLocaleString()}  ${p.lat},${p.lon}  acc=${p.acc||'?'}  batt=${p.batt ?? '?'}  ${p.provider||''}`);
    $('#gt20-points').textContent = rows.length ? rows.join('\n') : '无点位记录';
    const jl = await (await api('/api/logs?limit=200')).json();
    const lines = (jl.entries || []).filter(e => /location|geocode|paikka/i.test(e.msg))
      .map(e => `[${(e.time||'').replace('T',' ').replace(/\+.*$/,'')}] ${e.level} ${e.msg} ${e.fields ? JSON.stringify(e.fields) : ''}`);
    $('#gt20-log').textContent = lines.length ? lines.join('\n') : '无相关日志';
    $('#gt20-state').textContent = '更新于 ' + new Date().toLocaleTimeString();
  } catch (e) { $('#gt20-state').textContent = '加载失败: ' + e.message; }
}
async function refreshReitti() {
  try {
    const j = await (await api('/api/reitti/movements?limit=30')).json();
    const rows = (j.movements || []).map(m =>
      `### ${m.ts || ''}  (session: ${m.session || '?'}, since: ${m.since || 'default'})\n${m.text || ''}`);
    $('#reitti-list').textContent = rows.length ? rows.join('\n\n') : '尚无落盘的 narrative（蒸馏触发时记录）';
    $('#reitti-state').textContent = '更新于 ' + new Date().toLocaleTimeString();
  } catch (e) { $('#reitti-state').textContent = '加载失败: ' + e.message; }
}
$('#btn-gt20-refresh').onclick = () => asyncAction($('#btn-gt20-refresh'), refreshGT20);
$('#btn-reitti-refresh').onclick = () => asyncAction($('#btn-reitti-refresh'), refreshReitti);
$('#btn-reitti-live').onclick = () => asyncAction($('#btn-reitti-live'), async () => {
  const j = await (await api('/api/reitti/movement')).json();
  $('#reitti-live-out').textContent = j.ok ? (j.text || '(空窗口)') : ('错误: ' + (j.error || 'unknown'));
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
    if (ex) ex.textContent = (charExpressions || []).length ? '表情：' + charExpressions.join(', ') : '';
  }).catch(() => {});
}

/* ---------- characters: self-contained packages ---------- */
let editingNew = false;
async function refreshChars() {
  const { characters } = await (await api('/api/characters')).json();
  const list = characters || [];
  const grid = $('#char-grid');
  if (!list.length) {
    grid.innerHTML = '<div class="meta">还没有角色，点「＋ 新建角色」开始。</div>';
  } else {
    grid.innerHTML = list.map((c) => `
      <div class="char-card ${c.name === settings.char ? 'on' : ''}" data-name="${c.name}">
        <img class="avatar sq" src="${imgURL(c.avatar_url) || 'data:image/gif;base64,R0lGODlhAQABAAAAACH5BAEKAAEALAAAAAABAAEAAAICTAEAOw=='}" alt="">
        <div>
          <div class="cc-name">${c.name}</div>
          <div class="cc-desc">${(c.description || '（暂无描述，点卡片后编辑）').replace(/</g, '&lt;')}</div>
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
  loadDistill();
}
function openEditor(name, isNew) {
  editingNew = !!isNew;
  $('#char-editor').classList.remove('hidden');
  $('#char-editor-title').textContent = isNew ? '新建角色' : '编辑角色';
  if (isNew) {
    $('#char-name').value = '';
    $('#char-desc').value = '';
    $('#char-avatar').src = 'data:image/gif;base64,R0lGODlhAQABAAAAACH5BAEKAAEALAAAAAABAAEAAAICTAEAOw==';
    $('#char-expr').textContent = '';
    $('#char-name').readOnly = false;
    $('#btn-char-delete').style.display = 'none';
  } else {
    api('/api/characters/' + encodeURIComponent(name)).then((r) => r.json()).then((j) => {
      $('#char-name').value = j.name || name;
      $('#char-desc').value = j.description || '';
      $('#char-avatar').src = imgURL(j.avatar_url) || 'data:image/gif;base64,R0lGODlhAQABAAAAACH5BAEKAAEALAAAAAABAAEAAAICTAEAOw==';
      $('#char-expr').textContent = (j.expressions || []).length ? '表情：' + j.expressions.join(', ') : '';
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
  if (!name) { alert('角色名不能为空'); return; }
  if (editingNew) {
    const r = await api('/api/characters', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ name, description: desc }),
    });
    if (!r.ok) { alert('创建失败：' + (await r.text())); return; }
  } else {
    await api('/api/characters/' + encodeURIComponent(name) + '/card', {
      method: 'PUT', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ description: desc }),
    });
  }
  $('#char-editor').classList.add('hidden');
  refreshChars();
  if (!editingNew && name === settings.char) syncChatHead();
};
$('#btn-char-delete').onclick = async () => {
  const name = $('#char-name').value.trim();
  if (!name) return;
  if (!confirm(`删除角色「${name}」？默认保留聊天数据（只删角色卡与头像）。`)) return;
  await api('/api/characters/' + encodeURIComponent(name), { method: 'DELETE' });
  $('#char-editor').classList.add('hidden');
  refreshChars();
};
$('#char-file').onchange = async (e) => {
  const f = e.target.files[0];
  if (!f) return;
  const name = $('#char-name').value.trim();
  if (!name) { alert('先填角色名'); e.target.value = ''; return; }
  const fd = new FormData();
  fd.append('file', f);
  const r = await api('/api/characters/' + encodeURIComponent(name) + '/avatar', { method: 'PUT', body: fd });
  const j = await r.json();
  if (j.avatar_url) { $('#char-avatar').src = imgURL(j.avatar_url); syncChatHead(); }
  e.target.value = '';
};

/* ---------- memory (server-backed) ---------- */
// The collection picker is a <select multiple>; settings keep one CSV string.
// memCollectionWanted holds the ids that should be selected once the async
// list from rag-mcp-server arrives (load and refresh can race).
let memCollectionWanted = [];
function applyCollectionSelection() {
  const sel = $('#mem-collection');
  if (!sel) return;
  const want = new Set(memCollectionWanted);
  for (const o of sel.options) o.selected = want.has(o.value);
  // A stored id missing from the fetched list must stay visible/selected so
  // a stale setting is never silently dropped (e.g. rag-mcp unreachable).
  const missing = memCollectionWanted.filter((id) => ![...sel.options].some((o) => o.value === id));
  for (const id of missing) {
    const o = document.createElement('option');
    o.value = id;
    o.textContent = id + '（不在列表）';
    o.selected = true;
    sel.appendChild(o);
  }
}
async function refreshCollections() {
  try {
    const list = await (await api('/api/mcp/collections')).json();
    const sel = $('#mem-collection');
    if (!Array.isArray(list) || !sel) return;
    if (!memCollectionWanted.length) {
      memCollectionWanted = Array.from(sel.selectedOptions).map((o) => o.value);
    }
    sel.innerHTML = list.map((c) => {
      const tags = [];
      if (!c.enabled) tags.push('disabled');
      if (c.exists === false) tags.push('missing');
      const label = `${c.name} · ${c.chunk_count ?? '?'} chunks${tags.length ? ' (' + tags.join(', ') + ')' : ''}`;
      const disabled = (!c.enabled || c.exists === false) ? ' disabled' : '';
      return `<option value="${c.name}"${disabled}>${label}</option>`;
    }).join('');
    applyCollectionSelection();
  } catch { /* rag-mcp unreachable: keep whatever selection exists readable */ }
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
    $('#set-key-state').textContent = s.api_key_set ? ('API Key 已设置 ' + (s.api_key_hint || '')) : 'API Key 未设置';
    $('#mem-url').value = s.mcp_url || '';
    memCollectionWanted = (s.mcp_collection || '').split(',').map((x) => x.trim()).filter(Boolean);
    applyCollectionSelection();
    $('#mem-topk').value = s.mcp_topk ?? 10;
    $('#mem-budget').value = s.mcp_budget_tokens ?? 2000;
    $('#mem-perhit').value = s.mcp_per_hit_chars ?? 2000;
    $('#mem-timeout').value = s.mcp_timeout ?? 180;
    $('#mem-threshold').value = s.mcp_threshold ?? -1;
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
async function saveMemory(silent) {
  const r = await api('/api/settings', {
    method: 'PUT', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      mcp_url: $('#mem-url').value.trim(),
      mcp_collection: Array.from($('#mem-collection').selectedOptions).map((o) => o.value).join(','),
      mcp_topk: +$('#mem-topk').value || 10,
      mcp_budget_tokens: +$('#mem-budget').value || 2000,
      mcp_per_hit_chars: +$('#mem-perhit').value || 2000,
      mcp_timeout: +$('#mem-timeout').value || 180,
      mcp_threshold: +$('#mem-threshold').value,
      mcp_enabled: $('#mem-enabled').checked,
    }),
  });
  $('#mem-test-out').textContent = '已保存。开“每轮自动检索”后，下次发送即注入。';
  if (!silent) toast('Memory 设置已保存');
}
$('#btn-mem-save').onclick = () => asyncAction($('#btn-mem-save'), () => saveMemory(false));
['mem-url', 'mem-collection', 'mem-topk', 'mem-budget', 'mem-perhit', 'mem-timeout', 'mem-threshold', 'mem-enabled'].forEach((id) => {
  const el = document.getElementById(id); if (el) el.addEventListener('change', () => saveMemory(true).catch((e) => toast('Memory 保存失败：' + e.message, 'err')));
});
$('#btn-mem-test').onclick = async () => {
  const q = $('#mem-test-q').value.trim() || '测试';
  $('#mem-test-out').textContent = '检索中…';
  try {
    const j = await (await api('/api/memory/search', {
      method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ query: q }),
    })).json();
    $('#mem-test-out').textContent = JSON.stringify(j, null, 2).slice(0, 4000);
  } catch (e) { $('#mem-test-out').textContent = '失败：' + e; }
};
async function saveNtfy(silent) {
  await api('/api/settings', {
    method: 'PUT', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ ntfy_url: $('#set-ntfy-url').value.trim(), ntfy_topic: $('#set-ntfy-topic').value.trim() }),
  });
  if (!silent) toast('推送设置已保存');
}
$('#btn-ntfy-save').onclick = () => asyncAction($('#btn-ntfy-save'), () => saveNtfy(false));
['set-ntfy-url', 'set-ntfy-topic'].forEach((id) => {
  const el = document.getElementById(id); if (el) el.addEventListener('change', () => saveNtfy(true).catch((e) => toast('推送保存失败：' + e.message, 'err')));
});

/* ---------- distilled memory ---------- */
let distillDefaultPrompt = '';
async function loadDistill() {
  try {
    const j = await (await api('/api/distilled?session=' + encodeURIComponent(settings.char))).json();
    distillDefaultPrompt = j.default_prompt || '';
    $('#dmem-enabled').checked = !!j.enabled;
    $('#dmem-interval').value = j.interval || 8;
    $('#dmem-maxchars').value = j.max_chars || 4000;
    $('#dmem-retain').value = j.retain_days || 3;
    $('#dmem-state-days').value = j.state_max_days || 30;
    $('#dmem-perday').value = j.max_log_per_day || 40;
    $('#dmem-entry').value = j.max_entry_chars || 300;
    $('#dmem-model').value = j.model || '';
    // Open editable prompt: always show the effective template, not a blank box.
    $('#dmem-prompt').value = j.prompt || distillDefaultPrompt;
    $('#dmem-prompt').placeholder = '蒸馏提示词（可直接编辑；点“恢复默认提示词”重置）';
    const m = j.meta || {};
    const days = j.days || 0;
    $('#dmem-meta').textContent = (m.runs ? `已运行 ${m.runs} 次 · 上次 ${(m.last_run || '').replace('T', ' ').replace(/\+.*$/, '')}` : '尚未蒸馏') + (days ? ` · ${days} 天日志` : '');
    $('#dmem-out').textContent = (j.sheet && j.sheet.trim()) ? j.sheet : '（还没有记录，攒够轮数或点“立即蒸馏”）';
  } catch (e) { toast('蒸馏信息加载失败：' + e.message, 'err'); }
}
async function saveDistill(silent) {
  await api('/api/settings', {
    method: 'PUT', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      distill_enabled: $('#dmem-enabled').checked,
      distill_interval: +$('#dmem-interval').value || 8,
      distill_max_chars: +$('#dmem-maxchars').value || 4000,
      distill_retain_days: +$('#dmem-retain').value || 3,
      distill_state_max_days: +$('#dmem-state-days').value || 30,
      distill_max_log_per_day: +$('#dmem-perday').value || 40,
      distill_max_entry_chars: +$('#dmem-entry').value || 300,
      distill_model: $('#dmem-model').value.trim(),
      distill_prompt: $('#dmem-prompt').value,
    }),
  });
  if (!silent) toast('蒸馏设置已保存');
}
$('#btn-dmem-save').onclick = () => asyncAction($('#btn-dmem-save'), () => saveDistill(false));
['dmem-enabled', 'dmem-interval', 'dmem-maxchars', 'dmem-retain', 'dmem-state-days', 'dmem-perday', 'dmem-entry', 'dmem-model', 'dmem-prompt'].forEach((id) => {
  const el = document.getElementById(id); if (el) el.addEventListener('change', () => saveDistill(true).catch((e) => toast('保存失败：' + e.message, 'err')));
});
$('#btn-dmem-run').onclick = () => asyncAction($('#btn-dmem-run'), async () => {
  $('#dmem-meta').textContent = '蒸馏中…（可能耗时数十秒，请勿关闭）';
  const r = await api('/api/distilled/run', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ session: settings.char }) });
  const j = await r.json();
  if (!j.ok) throw new Error(j.error || ('HTTP ' + r.status));
  toast('蒸馏完成');
  await loadDistill();
});
$('#btn-dmem-prompt-reset').onclick = () => {
  $('#dmem-prompt').value = distillDefaultPrompt;
  $('#dmem-out').textContent = distillDefaultPrompt;
  toast('已恢复内置默认提示词（点保存生效）');
};
$('#btn-dmem-prompt-view').onclick = () => loadDistill();
$('#btn-dmem-rebuild').onclick = () => asyncAction($('#btn-dmem-rebuild'), async () => {
  if (!confirm('从 chat.jsonl 的所有 distilled_memory 记录重建事实表？会先备份为 distilled.md.bak，不调用 LLM。')) return;
  const r = await api('/api/distilled/rebuild', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ session: settings.char }) });
  const j = await r.json();
  if (!j.ok) throw new Error(j.error || ('HTTP ' + r.status));
  toast(`回填完成：${j.sheets} 条记录 → ${j.days} 天`);
  await loadDistill();
});

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
  } catch { /* 上游未通时静默 */ }
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
      el.innerHTML = `⚠ 主模型「${st.model || '?'}」不可用：${st.error || '不在上游模型列表'}。` +
        (avail ? ` 可用：${avail}…` : '') +
        ` <a id="model-banner-fix">去设置</a>`;
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
  refreshUserAvatarGutters(); // user_name 改了首字块也要跟
  applyTheme();
  if (!silent) toast('偏好已保存');
  return r;
}
$('#btn-settings-save').onclick = () => asyncAction($('#btn-settings-save'), async () => { await saveSettings(false); await loadServerSettings(); refreshModels(); });
/* ---------- user avatar (global, settings page) ---------- */
{ const f = $('#user-avatar-file');
  if (f) f.addEventListener('change', async (e) => {
    const file = e.target.files[0];
    e.target.value = '';
    if (!file) return;
    if (!file.type.startsWith('image/')) { toast('选中的不是图片', 'err'); return; }
    if (file.size > 8 * 1024 * 1024) { toast('图片太大（上限8MB）', 'err', 4200); return; }
    const fd = new FormData();
    fd.append('file', file);
    try {
      const r = await api('/api/user/avatar/', { method: 'PUT', body: fd });
      const j = await r.json().catch(() => ({}));
      if (!r.ok) throw new Error((j && j.error) || ('HTTP ' + r.status));
      userAvatarURL = j.avatar_url || '';
      refreshUserAvatarGutters();
      toast('我的头像已更新');
    } catch (err) { toast('上传失败：' + (err.message || err), 'err', 4200); }
  });
}
{ const del = $('#btn-user-avatar-del');
  if (del) del.onclick = () => asyncAction(del, async () => {
    await api('/api/user/avatar/', { method: 'DELETE' });
    userAvatarURL = '';
    refreshUserAvatarGutters();
    toast('我的头像已删除，改用名字首字');
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
  settingsSaveTimer = setTimeout(() => saveSettings(true).catch((e) => toast('偏好保存失败：' + e.message, 'err')), 600);
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
  if (!(await bootAuth())) return; // gate: stop booting until logged in
  applyTheme();
  fillSettingsForm();
  applyUiScale();
  await loadServerSettings();
  refreshCollections();
  fillSettingsForm();
  applyUiScale();
  $('#chat-char-name').textContent = settings.char;
  applyAvatarSize();
  loadUserAvatar();
  loadBlocks();
  refreshChars();
  refreshModels();
  syncChatHead();
  loadHistory();
  subscribeChat();
  loadDistill();
}
init();
