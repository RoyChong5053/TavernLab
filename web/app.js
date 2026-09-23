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

const DEFAULT_TIERS = [8192, 16384, 32768];
let blocks = [];
const settings = Object.assign(
  {
    tiers: DEFAULT_TIERS, response_reserve: 4096, recent_chat_min_turns: 4,
    rerank_url: 'http://127.0.0.1:11437', char: 'Leer乐儿', model: '',
    stream: false, visible_turns: 10, user_name: 'RoyChong', avatar_px: 88,
  },
  store.get('settings', {}),
);
if (!Array.isArray(settings.tiers) || !settings.tiers.length) settings.tiers = DEFAULT_TIERS;

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
  blocks = await (await fetch('/api/blocks')).json();
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
    const r = await fetch('/api/blocks', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(blocks) });
    if (!r.ok) throw new Error('HTTP ' + r.status);
    setUnsaved(false);
    if (!quiet) toast('Blocks 已保存');
  } catch (e) {
    toast('Blocks 保存失败：' + e.message, 'err', 4200);
    setUnsaved(true);
  }
}
let dragId = null;
function renderBlocks() {
  const el = $('#blocks');
  el.innerHTML = '';
  [...blocks].sort((a, b) => a.order - b.order).forEach((b) => {
    const d = document.createElement('div');
    d.className = 'block';
    d.draggable = true;
    d.dataset.id = b.id;
    const bmax = (b.budget && b.budget.max) || 0;
    d.innerHTML = `
      <div class="hd">
        <label><span class="grip" title="只从这里拖动排序">⠿</span><input type="checkbox" data-id="${b.id}" data-k="enabled" ${b.enabled ? 'checked' : ''}> <b>${b.id}</b></label>
        <span>order <input type="number" data-id="${b.id}" data-k="order" value="${b.order}"> max <input type="number" data-id="${b.id}" data-k="max" value="${bmax}"></span>
      </div>
      <div class="meta">${b.role} · ${b.source.type}${b.source.collection ? ':' + b.source.collection : ''}</div>
      <textarea rows="2" data-id="${b.id}" data-k="template">${(b.template || '').replace(/</g, '&lt;')}</textarea>`;
    el.appendChild(d);
  });
  el.querySelectorAll('input,textarea').forEach((inp) => {
    inp.onchange = () => {
      const b = blocks.find((x) => x.id === inp.dataset.id);
      if (!b) return;
      const k = inp.dataset.k;
      if (k === 'enabled') b.enabled = inp.checked;
      else if (k === 'max') b.budget = Object.assign({}, b.budget, { max: +inp.value });
      else if (k === 'order') b.order = +inp.value;
      else b[k] = inp.value;
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
function parseTiers(s) {
  const t = String(s || '').split(',').map((x) => parseInt(x.trim(), 10)).filter((n) => n > 0);
  return t.length ? t : DEFAULT_TIERS;
}
function ctxCfg() {
  return { tiers: settings.tiers, response_reserve: settings.response_reserve, recent_chat_min_turns: settings.recent_chat_min_turns };
}
function budgetLine(res) {
  const tier = res.tier ? `${Math.round(res.tier / 1024)}k` : '?';
  const drop = (res.dropped || []).join(', ') || '无';
  const blocksTxt = (res.blocks || []).map((b) => `${b.id}:${b.tokens}${b.truncated ? '✂' : ''}`).join(' · ');
  const overflow = res.overflow ? ' · <span style="color:#ffb4b4">⚠ OVERFLOW（超过最大档）</span>' : '';
  return `<b>${res.total_tokens}</b> / ${res.budget_tokens} tokens · tier ${tier} · dropped: ${drop}${overflow}<br>${blocksTxt}`;
}
async function assemble() {
  const r = await fetch('/api/assemble', {
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
let historyShown = 0;
const seenMsgIds = new Set();
function renderMsg(role, text, who, prepend, images, id) {
  const d = document.createElement('div');
  d.className = 'msg ' + (role === 'user' ? 'user' : role === 'assistant' ? 'ai' : 'sys');
  d.dataset.role = role;
  if (id) { d.dataset.id = id; seenMsgIds.add(id); }
  const w = document.createElement('div');
  w.className = 'who';
  const defaultWho = role === 'user' ? '你' : role === 'assistant' ? settings.char : (role === 'distilled_memory' ? 'Distilled Memory' : '系统');
  w.textContent = who || defaultWho;
  const b = document.createElement('div');
  b.className = 'body';
  b.textContent = text;
  d.append(w, b);
  if (images && images.length) {
    const im = document.createElement('div');
    im.className = 'msg-images';
    images.forEach((p) => {
      const img = document.createElement('img');
      img.className = 'msg-img';
      img.src = p.startsWith('data:') ? p : '/chars/' + encodeURIComponent(settings.char) + '/' + p;
      im.appendChild(img);
    });
    d.appendChild(im);
  }
  const box = $('#chat');
  if (prepend && box.firstChild) box.insertBefore(d, box.firstChild);
  else { box.appendChild(d); if (!prepend) d.scrollIntoView({ block: 'end' }); }
  return d;
}
function addMsg(role, text, who, images) { return renderMsg(role, text, who, false, images); }
async function loadHistory() {
  historyShown = 0;
  seenMsgIds.clear();
  $('#chat').innerHTML = '';
  await loadEarlier();
  const box = $('#chat');
  box.scrollTop = box.scrollHeight;
}
async function loadEarlier() {
  const q = new URLSearchParams({ session: settings.char, limit: settings.visible_turns, before: historyShown });
  const j = await (await fetch('/api/history?' + q)).json();
  const msgs = j.messages || [];
  if (!msgs.length) { $('#btn-earlier').textContent = '没有更早了'; return; }
  const box = $('#chat');
  const oldH = box.scrollHeight;
  [...msgs].reverse().forEach((m) => renderMsg(m.role, m.text, null, true, m.images, m.id));
  historyShown += msgs.length;
  box.scrollTop = box.scrollHeight - oldH;
  $('#btn-earlier').textContent = j.has_more ? '↑ 加载更早' : '没有更早了';
}
$('#btn-earlier').onclick = loadEarlier;
// Live updates from the server hub (other tabs / the app / background jobs).
// Ids de-dupe; text-compare absorbs the optimistic bubble from send().
let chatES = null;
function subscribeChat() {
  if (chatES) chatES.close();
  chatES = new EventSource('/api/events?session=' + encodeURIComponent(settings.char));
  chatES.addEventListener('message', (ev) => {
    let m; try { m = JSON.parse(ev.data); } catch { return; }
    if (!m || !m.role) return;
    if (m.id && seenMsgIds.has(m.id)) return;
    const domRole = m.role === 'assistant' ? 'ai' : (m.role === 'user' ? 'user' : 'sys');
    const bodies = [...document.querySelectorAll('#chat .msg.' + domRole + ' .body')];
    if (bodies.length && bodies[bodies.length - 1].textContent === m.text) {
      if (m.id) seenMsgIds.add(m.id);
      return;
    }
    renderMsg(m.role, m.text, null, false, m.images, m.id);
    if (m.role === 'assistant' && m.text) classifyAndBadge(m.text);
  });
  chatES.onerror = () => { /* browser auto-reconnects */ };
}
async function classifyAndBadge(text) {
  try {
    const r = await fetch('/api/expression/classify', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ text: text.slice(-500), rerank_url: settings.rerank_url }),
    });
    const j = await r.json();
    $('#expr-badge').textContent = j.fallback ? '心情 · 😐 平静' : '心情 · 😊 ' + j.label;
    $('#expr-badge').title = JSON.stringify(j.scores || {});
  } catch { /* 静默：表情失败不打断聊天 */ }
}
/* ---------- image attach ---------- */
let pendingImages = [];
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
  if (f) {
    pendingImages = [await new Promise((res) => { const r = new FileReader(); r.onload = () => res(r.result); r.readAsDataURL(f); })];
  }
  renderImgPreview();
  e.target.value = '';
};

async function send() {
  const ta = $('#input');
  const text = ta.value.trim();
  const imgs = pendingImages.slice();
  if (!text && !imgs.length) return;
  addMsg('user', text, null, imgs);
  ta.value = '';
  pendingImages = [];
  renderImgPreview();
  setPending(true);
  const stream = !!settings.stream;
  const body = { model: settings.model || undefined, session: settings.char, text, images: imgs, stream, blocks, context: ctxCfg() };
  try {
    if (stream) {
      const r = await fetch('/v1/chat/completions', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
      if (!r.ok || !r.body) throw new Error('HTTP ' + r.status + ' ' + (await r.text()).slice(0, 200));
      const reader = r.body.getReader();
      const dec = new TextDecoder();
      let buf = '', full = '';
      const box = addMsg('assistant', '');
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
          try { full += JSON.parse(payload).choices?.[0]?.delta?.content || ''; belly.textContent = full; } catch (err) { console.warn('sse parse', err, payload); }
        }
      }
      belly.classList.remove('cursor');
      if (!full.trim()) { belly.textContent = '（空回复）'; }
      setPending(false);
      if (full.trim()) classifyAndBadge(full);
      return;
    }
    const r = await fetch('/v1/chat/completions', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
    const j = await r.json().catch(() => ({}));
    if (!r.ok) throw new Error('HTTP ' + r.status + ' ' + JSON.stringify(j).slice(0, 200));
    const reply = j.choices?.[0]?.message?.content || '（无内容）';
    addMsg('assistant', reply);
    setPending(false);
    classifyAndBadge(reply);
  } catch (e) {
    setPending(false);
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
    pendingImages = [await new Promise((res) => { const r = new FileReader(); r.onload = () => res(r.result); r.readAsDataURL(f); })];
  }
  renderImgPreview();
});
$('#btn-assemble').onclick = assemble;
$('#btn-preview').onclick = async () => {
  const res = await assemble();
  if (!res) return;
  const m = res.memory || {};
  $('#preview-memory').textContent = m.enabled
    ? `MCP: ${m.collection} · query=「${(m.query || '').slice(0, 60)}」 · hits=${m.hits ?? '?'}${m.error ? ' · ⚠ ' + m.error : ''}`
    : 'MCP 未启用：本轮无记忆注入';
  $('#preview-blocks').innerHTML = budgetLine(res);
  $('#preview-text').textContent = res.prompt_text || '(空)';
  $('#preview-modal').classList.remove('hidden');
};
$('#btn-preview-close').onclick = () => $('#preview-modal').classList.add('hidden');
$('#preview-modal').addEventListener('click', (e) => { if (e.target.id === 'preview-modal') e.target.classList.add('hidden'); });
$('#btn-save-blocks').onclick = () => asyncAction($('#btn-save-blocks'), async () => { await saveBlocks(false); assemble(); });
$('#btn-reload-blocks').onclick = () => asyncAction($('#btn-reload-blocks'), async () => { await loadBlocks(); toast('已重载'); });

/* ---------- export / archive (moved to settings) ---------- */
$('#btn-export').onclick = () => {
  const q = new URLSearchParams({ session: settings.char, user: settings.user_name || 'user' });
  window.open('/api/export?' + q, '_blank');
};
$('#btn-export-all').onclick = () => {
  const q = new URLSearchParams({ session: settings.char, user: settings.user_name || 'user', archives: '1' });
  window.open('/api/export?' + q, '_blank');
};
$('#btn-archive').onclick = async () => {
  if (!confirm(`归档「${settings.char}」当前楼并另起新楼？归档进该角色数据包，聊天数据不会丢。`)) return;
  const j = await (await fetch('/api/archive', {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ session: settings.char }),
  })).json();
  if (j.ok) { loadHistory(); } else alert('归档失败。');
};

/* ---------- audit ---------- */
async function refreshAudit() {
  const { ids } = await (await fetch('/api/audit')).json();
  $('#audit-list').innerHTML = ids.map((id) => `<option>${id}</option>`).join('');
  if (ids.length) { $('#audit-list').value = ids[0]; viewAudit(); }
  else $('#audit').textContent = '暂无记录，先去聊一句。';
}
async function viewAudit() {
  const id = $('#audit-list').value;
  if (!id) return;
  $('#audit').textContent = JSON.stringify(await (await fetch('/api/audit/' + id)).json(), null, 2);
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
  const j = await (await fetch('/api/logs?limit=400')).json();
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
  logES = new EventSource('/api/logs/stream');
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
  await fetch('/api/logs', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ level: $('#log-level').value }) });
  openLogs();
};
$('#btn-log-copy').onclick = () => asyncAction($('#btn-log-copy'), async () => {
  await navigator.clipboard.writeText($('#log-view').innerText);
  toast('日志已复制');
});

/* ---------- chat avatar size (global) ---------- */
function applyAvatarSize() {
  const px = Math.min(240, Math.max(32, +settings.avatar_px || 88));
  $('#chat-avatar').style.width = px + 'px';
  $('#chat-avatar').style.height = px + 'px';
}
function syncChatHead() {
  $('#chat-char-name').textContent = settings.char;
  fetch('/api/characters/' + encodeURIComponent(settings.char)).then((r) => r.json()).then((j) => {
    $('#chat-avatar').src = j.avatar_url || 'data:image/gif;base64,R0lGODlhAQABAAAAACH5BAEKAAEALAAAAAABAAEAAAICTAEAOw==';
  }).catch(() => {});
}

/* ---------- characters: self-contained packages ---------- */
let editingNew = false;
async function refreshChars() {
  const { characters } = await (await fetch('/api/characters')).json();
  const list = characters || [];
  const grid = $('#char-grid');
  if (!list.length) {
    grid.innerHTML = '<div class="meta">还没有角色，点「＋ 新建角色」开始。</div>';
  } else {
    grid.innerHTML = list.map((c) => `
      <div class="char-card ${c.name === settings.char ? 'on' : ''}" data-name="${c.name}">
        <img class="avatar sq" src="${c.avatar_url || 'data:image/gif;base64,R0lGODlhAQABAAAAACH5BAEKAAEALAAAAAABAAEAAAICTAEAOw=='}" alt="">
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
  await fetch('/api/settings', {
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
    fetch('/api/characters/' + encodeURIComponent(name)).then((r) => r.json()).then((j) => {
      $('#char-name').value = j.name || name;
      $('#char-desc').value = j.description || '';
      $('#char-avatar').src = j.avatar_url || 'data:image/gif;base64,R0lGODlhAQABAAAAACH5BAEKAAEALAAAAAABAAEAAAICTAEAOw==';
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
    const r = await fetch('/api/characters', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ name, description: desc }),
    });
    if (!r.ok) { alert('创建失败：' + (await r.text())); return; }
  } else {
    await fetch('/api/characters/' + encodeURIComponent(name) + '/card', {
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
  await fetch('/api/characters/' + encodeURIComponent(name), { method: 'DELETE' });
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
  const r = await fetch('/api/characters/' + encodeURIComponent(name) + '/avatar', { method: 'PUT', body: fd });
  const j = await r.json();
  if (j.avatar_url) { $('#char-avatar').src = j.avatar_url; syncChatHead(); }
  e.target.value = '';
};

/* ---------- memory (server-backed) ---------- */
async function loadServerSettings() {
  try {
    const s = await (await fetch('/api/settings')).json();
    if (s.upstream) $('#set-upstream').value = s.upstream;
    if (s.rerank_url) $('#set-rerank').value = s.rerank_url;
    $('#set-key-state').textContent = s.api_key_set ? ('API Key 已设置 ' + (s.api_key_hint || '')) : 'API Key 未设置';
    $('#mem-url').value = s.mcp_url || '';
    $('#mem-collection').value = s.mcp_collection || '';
    $('#mem-topk').value = s.mcp_topk ?? 10;
    $('#mem-timeout').value = s.mcp_timeout ?? 120;
    $('#mem-threshold').value = s.mcp_threshold ?? -1;
    $('#mem-enabled').checked = !!s.mcp_enabled;
    if (s.current_char && s.current_char !== settings.char) { settings.char = s.current_char; store.set('settings', settings); }
    if (s.user_name) $('#set-user').value = s.user_name;
    $('#set-ntfy-url').value = s.ntfy_url || '';
    $('#set-ntfy-topic').value = s.ntfy_topic || '';
    return s;
  } catch { return null; }
}
async function saveMemory(silent) {
  const r = await fetch('/api/settings', {
    method: 'PUT', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      mcp_url: $('#mem-url').value.trim(),
      mcp_collection: $('#mem-collection').value.trim(),
      mcp_topk: +$('#mem-topk').value || 10,
      mcp_timeout: +$('#mem-timeout').value || 120,
      mcp_threshold: +$('#mem-threshold').value,
      mcp_enabled: $('#mem-enabled').checked,
    }),
  });
  $('#mem-test-out').textContent = '已保存。开“每轮自动检索”后，下次发送即注入。';
  if (!silent) toast('Memory 设置已保存');
}
$('#btn-mem-save').onclick = () => asyncAction($('#btn-mem-save'), () => saveMemory(false));
['mem-url', 'mem-collection', 'mem-topk', 'mem-timeout', 'mem-threshold', 'mem-enabled'].forEach((id) => {
  const el = document.getElementById(id); if (el) el.addEventListener('change', () => saveMemory(true).catch((e) => toast('Memory 保存失败：' + e.message, 'err')));
});
$('#btn-mem-test').onclick = async () => {
  const q = $('#mem-test-q').value.trim() || '测试';
  $('#mem-test-out').textContent = '检索中…';
  try {
    const j = await (await fetch('/api/memory/search', {
      method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ query: q }),
    })).json();
    $('#mem-test-out').textContent = JSON.stringify(j, null, 2).slice(0, 4000);
  } catch (e) { $('#mem-test-out').textContent = '失败：' + e; }
};
async function saveNtfy(silent) {
  await fetch('/api/settings', {
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
    const j = await (await fetch('/api/distilled?session=' + encodeURIComponent(settings.char))).json();
    distillDefaultPrompt = j.default_prompt || '';
    $('#dmem-enabled').checked = !!j.enabled;
    $('#dmem-interval').value = j.interval || 8;
    $('#dmem-maxchars').value = j.max_chars || 4000;
    $('#dmem-retain').value = j.retain_days || 3;
    $('#dmem-model').value = j.model || '';
    // Open editable prompt: always show the effective template, not a blank box.
    $('#dmem-prompt').value = j.prompt || distillDefaultPrompt;
    $('#dmem-prompt').placeholder = '蒸馏提示词（可直接编辑；点“恢复默认提示词”重置）';
    const m = j.meta || {};
    $('#dmem-meta').textContent = m.runs ? `已运行 ${m.runs} 次 · 上次 ${(m.last_run || '').replace('T', ' ').replace(/\+.*$/, '')}` : '尚未蒸馏';
    $('#dmem-out').textContent = (j.sheet && j.sheet.trim()) ? j.sheet : '（还没有事实表，攒够轮数或点“立即蒸馏”）';
  } catch (e) { toast('蒸馏信息加载失败：' + e.message, 'err'); }
}
async function saveDistill(silent) {
  await fetch('/api/settings', {
    method: 'PUT', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      distill_enabled: $('#dmem-enabled').checked,
      distill_interval: +$('#dmem-interval').value || 8,
      distill_max_chars: +$('#dmem-maxchars').value || 4000,
      distill_retain_days: +$('#dmem-retain').value || 3,
      distill_model: $('#dmem-model').value.trim(),
      distill_prompt: $('#dmem-prompt').value,
    }),
  });
  if (!silent) toast('蒸馏设置已保存');
}
$('#btn-dmem-save').onclick = () => asyncAction($('#btn-dmem-save'), () => saveDistill(false));
['dmem-enabled', 'dmem-interval', 'dmem-maxchars', 'dmem-retain', 'dmem-model', 'dmem-prompt'].forEach((id) => {
  const el = document.getElementById(id); if (el) el.addEventListener('change', () => saveDistill(true).catch((e) => toast('保存失败：' + e.message, 'err')));
});
$('#btn-dmem-run').onclick = () => asyncAction($('#btn-dmem-run'), async () => {
  $('#dmem-meta').textContent = '蒸馏中…（可能耗时数十秒，请勿关闭）';
  const r = await fetch('/api/distilled/run', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ session: settings.char }) });
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

/* ---------- models ---------- */
async function refreshModels() {
  try {
    const j = await (await fetch('/api/models')).json();
    const ids = (j.data || []).map((m) => m.id).filter(Boolean);
    const sel = $('#set-model');
    sel.innerHTML = ids.map((id) => `<option value="${id}">${id}</option>`).join('');
    if (ids.includes(settings.model)) sel.value = settings.model;
    else if (ids.length) { sel.value = ids[0]; settings.model = ids[0]; store.set('settings', settings); }
  } catch { /* 上游未通时静默 */ }
}
$('#btn-models-refresh').onclick = refreshModels;
$('#set-model').onchange = (e) => { settings.model = e.target.value; store.set('settings', settings); };

/* ---------- settings ---------- */
function fillSettingsForm() {
  $('#set-tiers').value = settings.tiers.join(',');
  $('#set-reserve').value = settings.response_reserve;
  $('#set-minrounds').value = settings.recent_chat_min_turns;
  $('#set-rerank').value = settings.rerank_url;
  $('#set-stream').checked = !!settings.stream;
  $('#set-visible').value = settings.visible_turns || 10;
  $('#set-user').value = settings.user_name || '';
  $('#set-avatar-size').value = settings.avatar_px || 88;
}
async function saveSettings(silent) {
  settings.tiers = parseTiers($('#set-tiers').value);
  settings.response_reserve = +$('#set-reserve').value || 4096;
  settings.recent_chat_min_turns = +$('#set-minrounds').value || 4;
  settings.rerank_url = $('#set-rerank').value.trim() || settings.rerank_url;
  settings.stream = $('#set-stream').checked;
  settings.visible_turns = Math.min(200, Math.max(5, +$('#set-visible').value || 10));
  settings.avatar_px = Math.min(240, Math.max(32, +$('#set-avatar-size').value || 88));
  settings.user_name = $('#set-user').value.trim() || 'user';
  settings.model = $('#set-model').value || settings.model;
  store.set('settings', settings);
  const r = await fetch('/api/settings', {
    method: 'PUT', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      upstream: $('#set-upstream').value.trim(),
      api_key: $('#set-apikey').value,
      rerank_url: $('#set-rerank').value.trim(),
      user_name: settings.user_name,
    }),
  });
  if (!r.ok) throw new Error('HTTP ' + r.status);
  $('#set-apikey').value = '';
  applyAvatarSize();
  if (!silent) toast('偏好已保存');
  return r;
}
$('#btn-settings-save').onclick = () => asyncAction($('#btn-settings-save'), async () => { await saveSettings(false); await loadServerSettings(); refreshModels(); });
// autosave: any change in the settings form persists (debounced)
let settingsSaveTimer = null;
function scheduleSettingsSave() {
  clearTimeout(settingsSaveTimer);
  settingsSaveTimer = setTimeout(() => saveSettings(true).catch((e) => toast('偏好保存失败：' + e.message, 'err')), 600);
}
['set-tiers', 'set-reserve', 'set-minrounds', 'set-rerank', 'set-stream', 'set-visible', 'set-avatar-size', 'set-user', 'set-upstream', 'set-apikey']
  .forEach((id) => { const el = document.getElementById(id); if (el) el.addEventListener('change', scheduleSettingsSave); });

/* ---------- cross-device refresh on tab focus ---------- */
document.addEventListener('visibilitychange', () => {
  if (!document.hidden) { loadHistory(); subscribeChat(); }
});

/* ---------- init ---------- */
async function init() {
  fillSettingsForm();
  await loadServerSettings();
  fillSettingsForm();
  $('#chat-char-name').textContent = settings.char;
  applyAvatarSize();
  loadBlocks();
  refreshChars();
  refreshModels();
  syncChatHead();
  loadHistory();
  subscribeChat();
  loadDistill();
}
init();
