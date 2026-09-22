// leer-chat P0 frontend: vanilla JS, no build (rclone-friendly).
// Borrowed UX from llama.cpp server UI: glass + mobile drawers + SSE stream.
// Internal structure is our own: Blocks | Chat | Audit.
const $ = (s) => document.querySelector(s);
let blocks = [];

async function loadBlocks() {
  const r = await fetch('/api/blocks');
  blocks = await r.json();
  renderBlocks();
}
function renderBlocks() {
  const el = $('#blocks');
  el.innerHTML = '';
  blocks.sort((a, b) => a.order - b.order).forEach((b, i) => {
    const d = document.createElement('div');
    d.className = 'block';
    d.innerHTML = `
      <div class="hd">
        <label><input type="checkbox" data-i="${i}" data-k="enabled" ${b.enabled ? 'checked' : ''}> <b>${b.id}</b></label>
        <span><input type="number" data-i="${i}" data-k="order" value="${b.order}" title="order=位置"> <input type="number" data-i="${i}" data-k="priority" value="${b.priority}" title="priority=压缩顺序"></span>
      </div>
      <div class="meta">${b.role} · ${b.source.type}${b.source.collection ? ':' + b.source.collection : ''} · budget ${b.budget.min}/${b.budget.max || '∞'}</div>
      <textarea rows="2" data-i="${i}" data-k="template">${b.template || ''}</textarea>`;
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
function chatTurns() {
  return [...document.querySelectorAll('#chat .msg')].map((m) => ({
    role: m.dataset.role || 'user', content: m.textContent,
  }));
}
async function assemble() {
  const r = await fetch('/api/assemble', {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ blocks, context: { max_tokens: 16384, response_reserve: 4096, recent_chat_min_turns: 4 }, chat: chatTurns() }),
  });
  const res = await r.json();
  $('#budget').innerHTML = `<b>${res.total_tokens}</b> / ${res.budget_tokens} tokens · dropped: ${(res.dropped || []).join(', ') || '无'}` +
    '<br>' + (res.blocks || []).map((b) => `${b.id}:${b.tokens}${b.truncated ? '✂' : ''}`).join(' · ');
  return res;
}
function addMsg(role, text, cls) {
  const d = document.createElement('div');
  d.className = 'msg ' + (cls || role);
  d.dataset.role = role;
  d.textContent = text;
  $('#chat').appendChild(d);
  d.scrollIntoView();
}
async function send() {
  const text = $('#input').value.trim();
  if (!text) return;
  addMsg('user', text);
  $('#input').value = '';
  const stream = $('#stream').checked;
  const body = { model: $('#model').value, session: $('#session').value, stream, blocks, context: { max_tokens: 16384, response_reserve: 4096, recent_chat_min_turns: 4 } };
  if (stream) {
    const r = await fetch('/v1/chat/completions', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
    const reader = r.body.getReader();
    const dec = new TextDecoder();
    let buf = '', full = '';
    const box = (() => { addMsg('assistant', '…'); return $('#chat').lastChild; })();
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
          const t = j.choices?.[0]?.delta?.content || '';
          full += t;
          box.textContent = full;
        } catch {}
      }
    }
    refreshAudit();
    return;
  }
  const r = await fetch('/v1/chat/completions', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
  const j = await r.json();
  addMsg('assistant', j.choices?.[0]?.message?.content || JSON.stringify(j).slice(0, 2000));
  refreshAudit();
}
async function refreshAudit() {
  const r = await fetch('/api/audit');
  const { ids } = await r.json();
  const sel = $('#audit-list');
  sel.innerHTML = ids.map((id) => `<option>${id}</option>`).join('');
  if (ids.length) { sel.value = ids[0]; viewAudit(); }
}
async function viewAudit() {
  const id = $('#audit-list').value;
  if (!id) return;
  const r = await fetch('/api/audit/' + id);
  $('#audit').textContent = JSON.stringify(await r.json(), null, 2);
}
$('#btn-assemble').onclick = assemble;
$('#btn-send').onclick = send;
$('#btn-send2').onclick = send;
$('#btn-save-blocks').onclick = async () => {
  await fetch('/api/blocks', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(blocks) });
  assemble();
};
$('#btn-reload-blocks').onclick = loadBlocks;
$('#btn-audit-list').onclick = refreshAudit;
$('#btn-audit-view').onclick = viewAudit;
document.querySelectorAll('.mobilebar button').forEach((b) => {
  b.onclick = () => {
    document.querySelectorAll('.mobilebar button').forEach((x) => x.classList.remove('on'));
    b.classList.add('on');
    ['col-blocks', 'col-chat', 'col-audit'].forEach((id) => document.getElementById(id).classList.remove('on'));
    document.getElementById(b.dataset.tab).classList.add('on');
  };
});
loadBlocks();
refreshAudit();
