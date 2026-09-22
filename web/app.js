// leer-chat P1 frontend: vanilla JS, no build (rclone-friendly).
// Layout: left sidebar nav (llama.cpp-style) + one page at a time.
// Chat is default. Avatar <img> plays animated webp natively.
const $ = (s) => document.querySelector(s);
const store = {
  get(k, d) { try { const v = localStorage.getItem('leerchat.' + k); return v === null ? d : JSON.parse(v); } catch { return d; } },
  set(k, v) { localStorage.setItem('leerchat.' + k, JSON.stringify(v)); },
};
let blocks = [];
const settings = Object.assign(
  { max_tokens: 16384, response_reserve: 4096, recent_chat_min_turns: 4, rerank_url: 'http://127.0.0.1:11437', char: 'Leer乐儿' },
  store.get('settings', {}),
);

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
  renderBlocks();
}
function renderBlocks() {
  const el = $('#blocks');
  el.innerHTML = '';
  [...blocks].sort((a, b) => a.order - b.order).forEach((b) => {
    const i = blocks.indexOf(b);
    const d = document.createElement('div');
    d.className = 'block';
    d.innerHTML = `
      <div class="hd">
        <label><input type="checkbox" data-i="${i}" data-k="enabled" ${b.enabled ? 'checked' : ''}> <b>${b.id}</b></label>
        <span>order <input type="number" data-i="${i}" data-k="order" value="${b.order}"> pri <input type="number" data-i="${i}" data-k="priority" value="${b.priority}"></span>
      </div>
      <div class="meta">${b.role} · ${b.source.type}${b.source.collection ? ':' + b.source.collection : ''} · budget ${b.budget.min}/${b.budget.max || '∞'}</div>
      <textarea rows="2" data-i="${i}" data-k="template">${(b.template || '').replace(/</g, '&lt;')}</textarea>`;
    el.appendChild(d);
  });
  el.querySelectorAll('input,textarea').forEach((inp) => {
    inp.onchange = () => {
      const i = +inp.dataset.i, k = inp.dataset.k;
      if (k === 'enabled') blocks[i][k] = inp.checked;
      else if (k === 'order' || k === 'priority') blocks[i][k] = +inp.value;
      else blocks[i][k] = inp.value;
    };
  });
}
function ctxCfg() {
  return { max_tokens: settings.max_tokens, response_reserve: settings.response_reserve, recent_chat_min_turns: settings.recent_chat_min_turns };
}
async function assemble() {
  const r = await fetch('/api/assemble', {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ blocks, context: ctxCfg(), chat: chatTurns() }),
  });
  const res = await r.json();
  $('#budget').innerHTML = `<b>${res.total_tokens}</b> / ${res.budget_tokens} tokens · dropped: ${(res.dropped || []).join(', ') || '无'}` +
    '<br>' + (res.blocks || []).map((b) => `${b.id}:${b.tokens}${b.truncated ? '✂' : ''}`).join(' · ');
  return res;
}

/* ---------- chat ---------- */
function chatTurns() {
  return [...document.querySelectorAll('#chat .msg')].map((m) => ({ role: m.dataset.role || 'user', content: m.querySelector('.body').textContent }));
}
function addMsg(role, text, who) {
  const d = document.createElement('div');
  d.className = 'msg ' + (role === 'user' ? 'user' : role === 'assistant' ? 'ai' : 'sys');
  d.dataset.role = role;
  const w = document.createElement('div');
  w.className = 'who';
  w.textContent = who || (role === 'user' ? '你' : role === 'assistant' ? settings.char : '系统');
  const b = document.createElement('div');
  b.className = 'body';
  b.textContent = text;
  d.append(w, b);
  $('#chat').appendChild(d);
  d.scrollIntoView({ block: 'end' });
  return d;
}
async function classifyAndBadge(text) {
  try {
    const r = await fetch('/api/expression/classify', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ text: text.slice(-500), rerank_url: settings.rerank_url }),
    });
    const j = await r.json();
    $('#expr-badge').textContent = j.fallback ? '😐 默认' : '😊 ' + j.label;
    $('#expr-badge').title = JSON.stringify(j.scores || {});
  } catch { /* 静默：表情失败不打断聊天 */ }
}
async function send() {
  const ta = $('#input');
  const text = ta.value.trim();
  if (!text) return;
  addMsg('user', text);
  ta.value = '';
  const stream = $('#stream').checked;
  const body = { model: $('#model').value, session: $('#session').value, stream, blocks, context: ctxCfg() };
  if (stream) {
    const r = await fetch('/v1/chat/completions', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
    const reader = r.body.getReader();
    const dec = new TextDecoder();
    let buf = '', full = '';
    const box = addMsg('assistant', '…');
    const belly = box.querySelector('.body');
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
        try { full += JSON.parse(payload).choices?.[0]?.delta?.content || ''; belly.textContent = full; } catch {}
      }
    }
    classifyAndBadge(full);
    return;
  }
  const r = await fetch('/v1/chat/completions', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
  const j = await r.json();
  const reply = j.choices?.[0]?.message?.content || JSON.stringify(j).slice(0, 2000);
  addMsg('assistant', reply);
  classifyAndBadge(reply);
}
$('#btn-send').onclick = send;
$('#input').addEventListener('keydown', (e) => {
  if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); send(); }
});
$('#btn-assemble').onclick = assemble;
$('#btn-save-blocks').onclick = async () => {
  await fetch('/api/blocks', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(blocks) });
  assemble();
};
$('#btn-reload-blocks').onclick = loadBlocks;

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

/* ---------- characters ---------- */
async function refreshChars() {
  const { characters } = await (await fetch('/api/characters')).json();
  const sel = $('#char-list');
  sel.innerHTML = characters.map((c) => `<option>${c}</option>`).join('');
  if (!characters.includes(settings.char) && characters.length) settings.char = characters[0];
  if (characters.length) { sel.value = settings.char; $('#char-name').value = settings.char; viewChar(); }
  syncChatHead();
}
async function viewChar() {
  const name = $('#char-name').value.trim() || $('#char-list').value;
  if (!name) return;
  const j = await (await fetch('/api/characters/' + encodeURIComponent(name))).json();
  $('#char-avatar').src = j.avatar_url || '';
  $('#char-expr').textContent = j.avatar_url ? `头像：${j.avatar_url} · 表情(${j.expressions.length})：${j.expressions.join(', ') || '暂无，P2接Expression Router'}` : '暂无头像，上传一张 webp（动图直播）。';
}
function syncChatHead() {
  $('#chat-char-name').textContent = settings.char;
  const name = settings.char;
  fetch('/api/characters/' + encodeURIComponent(name)).then((r) => r.json()).then((j) => { $('#chat-avatar').src = j.avatar_url || ''; }).catch(() => {});
}
$('#btn-char-refresh').onclick = refreshChars;
$('#char-list').onchange = (e) => { $('#char-name').value = e.target.value; settings.char = e.target.value; store.set('settings', settings); viewChar(); syncChatHead(); };
$('#btn-char-new').onclick = () => { const n = $('#char-name').value.trim(); if (n) { settings.char = n; store.set('settings', settings); viewChar(); syncChatHead(); } };
$('#char-file').onchange = async (e) => {
  const f = e.target.files[0];
  if (!f) return;
  const name = $('#char-name').value.trim() || 'Leer乐儿';
  const fd = new FormData();
  fd.append('file', f);
  const r = await fetch('/api/characters/' + encodeURIComponent(name) + '/avatar', { method: 'PUT', body: fd });
  const j = await r.json();
  if (j.avatar_url) { $('#char-avatar').src = j.avatar_url; syncChatHead(); refreshChars(); }
  e.target.value = '';
};

/* ---------- memory stub ---------- */
['mem-vectra', 'mem-mcp', 'mem-mcp-on', 'mem-topk'].forEach((id) => {
  const v = store.get('memory', {})[id];
  if (v !== undefined) { const el = document.getElementById(id); if (el.type === 'checkbox') el.checked = v; else el.value = v; }
  document.getElementById(id).onchange = (e) => {
    const m = store.get('memory', {});
    m[id] = e.target.type === 'checkbox' ? e.target.checked : e.target.value;
    store.set('memory', m);
  };
});

/* ---------- settings ---------- */
$('#set-max').value = settings.max_tokens;
$('#set-reserve').value = settings.response_reserve;
$('#set-minrounds').value = settings.recent_chat_min_turns;
$('#set-rerank').value = settings.rerank_url;
$('#btn-settings-save').onclick = () => {
  settings.max_tokens = +$('#set-max').value || 16384;
  settings.response_reserve = +$('#set-reserve').value || 4096;
  settings.recent_chat_min_turns = +$('#set-minrounds').value || 4;
  settings.rerank_url = $('#set-rerank').value.trim() || settings.rerank_url;
  store.set('settings', settings);
};

/* ---------- init ---------- */
$('#chat-char-name').textContent = settings.char;
loadBlocks();
refreshChars();
