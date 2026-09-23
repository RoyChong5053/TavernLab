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
    d.draggable = true;
    d.dataset.i = i;
    d.innerHTML = `
      <div class="hd">
        <label><span class="grip" title="拖动排序">⠿</span><input type="checkbox" data-i="${i}" data-k="enabled" ${b.enabled ? 'checked' : ''}> <b>${b.id}</b></label>
        <span>order <input type="number" data-i="${i}" data-k="order" value="${b.order}"> max <input type="number" data-i="${i}" data-k="max" value="${b.budget.max || 0}"></span>
      </div>
      <div class="meta">${b.role} · ${b.source.type}${b.source.collection ? ':' + b.source.collection : ''}</div>
      <textarea rows="2" data-i="${i}" data-k="template">${(b.template || '').replace(/</g, '&lt;')}</textarea>`;
    el.appendChild(d);
  });
  el.querySelectorAll('input,textarea').forEach((inp) => {
    inp.onchange = () => {
      const i = +inp.dataset.i, k = inp.dataset.k;
      if (k === 'enabled') blocks[i][k] = inp.checked;
      else if (k === 'order' || k === 'max') {
        if (k === 'max') blocks[i].budget = Object.assign({}, blocks[i].budget, { max: +inp.value });
        else blocks[i][k] = +inp.value;
      } else blocks[i][k] = inp.value;
    };
  });
  // drag-drop: drop position decides order (renumbered sequentially)
  let dragI = null;
  el.querySelectorAll('.block').forEach((d) => {
    d.addEventListener('dragstart', () => { dragI = +d.dataset.i; d.classList.add('dragging'); });
    d.addEventListener('dragend', () => d.classList.remove('dragging'));
    d.addEventListener('dragover', (e) => { e.preventDefault(); d.classList.add('over'); });
    d.addEventListener('dragleave', () => d.classList.remove('over'));
    d.addEventListener('drop', (e) => {
      e.preventDefault();
      const toI = +d.dataset.i;
      if (dragI === null || dragI === toI) return;
      const ordered = [...blocks].sort((a, b) => a.order - b.order);
      const [mv] = ordered.splice(ordered.indexOf(blocks[dragI]), 1);
      ordered.splice(ordered.indexOf(blocks[toI]), 0, mv);
      ordered.forEach((b, n) => { b.order = n; });
      dragI = null;
      renderBlocks();
    });
  });
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
function renderMsg(role, text, who, prepend, images) {
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
  if (images && images.length) {
    const im = document.createElement('div');
    im.className = 'msg-images';
    images.forEach((p) => {
      const img = document.createElement('img');
      img.className = 'msg-img';
      img.src = '/chars/' + encodeURIComponent(settings.char) + '/' + p;
      im.appendChild(img);
    });
    d.appendChild(im);
  }
  const box = $('#chat');
  if (prepend && box.firstChild) box.insertBefore(d, box.firstChild);
  else { box.appendChild(d); d.scrollIntoView({ block: 'end' }); }
  return d;
}
function addMsg(role, text, who, images) { return renderMsg(role, text, who, false, images); }
async function loadHistory() {
  historyShown = 0;
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
  [...msgs].reverse().forEach((m) => renderMsg(m.role, m.text, null, true, m.images));
  historyShown += msgs.length;
  box.scrollTop = box.scrollHeight - oldH;
  $('#btn-earlier').textContent = j.has_more ? '↑ 加载更早' : '没有更早了';
}
$('#btn-earlier').onclick = loadEarlier;
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
async function send() {
  const ta = $('#input');
  const text = ta.value.trim();
  if (!text) return;
  addMsg('user', text);
  ta.value = '';
  const stream = !!settings.stream;
  const body = { model: settings.model || undefined, session: settings.char, text, stream, blocks, context: ctxCfg() };
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
$('#btn-save-blocks').onclick = async () => {
  await fetch('/api/blocks', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(blocks) });
  assemble();
};
$('#btn-reload-blocks').onclick = loadBlocks;

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
    $('#mem-threshold').value = s.mcp_threshold ?? -1;
    $('#mem-enabled').checked = !!s.mcp_enabled;
    if (s.current_char && s.current_char !== settings.char) { settings.char = s.current_char; store.set('settings', settings); }
    if (s.user_name) $('#set-user').value = s.user_name;
    $('#set-ntfy-url').value = s.ntfy_url || '';
    $('#set-ntfy-topic').value = s.ntfy_topic || '';
    return s;
  } catch { return null; }
}
$('#btn-mem-save').onclick = async () => {
  await fetch('/api/settings', {
    method: 'PUT', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      mcp_url: $('#mem-url').value.trim(),
      mcp_collection: $('#mem-collection').value.trim(),
      mcp_topk: +$('#mem-topk').value || 10,
      mcp_threshold: +$('#mem-threshold').value,
      mcp_enabled: $('#mem-enabled').checked,
    }),
  });
  $('#mem-test-out').textContent = '已保存。开“每轮自动检索”后，下次发送即注入。';
};
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
$('#btn-ntfy-save').onclick = async () => {
  await fetch('/api/settings', {
    method: 'PUT', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ ntfy_url: $('#set-ntfy-url').value.trim(), ntfy_topic: $('#set-ntfy-topic').value.trim() }),
  });
  alert('已保存推送设置。');
};

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
$('#btn-settings-save').onclick = async () => {
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
  await fetch('/api/settings', {
    method: 'PUT', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      upstream: $('#set-upstream').value.trim(),
      api_key: $('#set-apikey').value,
      rerank_url: $('#set-rerank').value.trim(),
      user_name: settings.user_name,
    }),
  });
  $('#set-apikey').value = '';
  applyAvatarSize();
  loadServerSettings();
  refreshModels();
};

/* ---------- cross-device refresh on tab focus ---------- */
document.addEventListener('visibilitychange', () => {
  if (!document.hidden) loadHistory();
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
}
init();
