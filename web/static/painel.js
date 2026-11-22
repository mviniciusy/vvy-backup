// Card do Drive: medição ao vivo, hover na legenda e limpeza da lixeira.
const $ = id => document.getElementById(id);
const icon = n => `<svg class="i" aria-hidden="true"><use href="#i-${n}"/></svg>`;
const esc = s => String(s).replace(/[&<>"]/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[c]));
const sleep = ms => new Promise(r => setTimeout(r, ms));
const cfg = JSON.parse($('cfg').textContent);
const bar = new PixelBar($('bar'));
let D = cfg.drive, cleaning = '', note = '', hover = -1;

function render(d) {
  D = d;
  const parts = d.parts || [];
  countTo($('used'), d.busy ? parts.reduce((s, p) => s + p.bytes, 0) : d.used, fmtBytes);
  $('of').textContent = d.total ? `de ${fmtBytes(d.total)} usados` : '';
  bar.set(parts, d.total || 1);
  $('legend').innerHTML = parts.map((p, i) =>
    `<li data-i="${i}" tabindex="0" title="${esc(p.name)}"><span class="ico" style="color:${p.color}">${icon(p.icon)}</span>` +
    `<span class="nm">${esc(p.name)}</span><span class="val">${fmtBytes(p.bytes)}</span></li>`).join('');
  $('rescan').disabled = d.busy || !!cleaning;
  sub(); foot();
  if (d.busy) setTimeout(poll, 800);
}

function sub() {
  const el = $('sub'), d = D, p = d.parts?.[hover];
  if (p) {
    el.innerHTML = `<span class="ico" style="color:${p.color}">${icon(p.icon)}</span><b>${esc(p.name)}</b>` +
      `<span class="dim">${fmtBytes(p.bytes)}</span><span class="dim" title="${esc(p.desc || '')}">${esc(p.desc || '')}</span>`;
  } else if (cleaning) {
    el.innerHTML = `<b>Limpando a lixeira</b><code>${esc(cleaning)}</code>`;
  } else if (d.busy) {
    el.innerHTML = `<b>Medindo</b><code>${esc(d.current || '')}</code>`;
  } else if (note) {
    el.innerHTML = note;
  } else if (d.err) {
    el.innerHTML = `<span class="err">${esc(d.err)}</span>`;
  } else {
    el.innerHTML = `<span class="dim">${fmtBytes(d.total - d.used)} livres</span>`;
  }
}

function foot() {
  const el = $('foot'), d = D;
  if (d.busy) {
    el.innerHTML = `<span class="ico spin">${icon('refresh')}</span><span class="dim">Procurando o que dá para limpar</span>`;
  } else if (cfg.trashJob && d.trash > 0) {
    el.innerHTML = `<span class="ico" style="color:#ef4444">${icon('trash')}</span>` +
      `<span>A lixeira do backup guarda ${fmtBytes(d.trash)}. O que tem mais de ${esc(cfg.trashAge)} pode sair.</span>` +
      `<button type="button" class="btn" id="clean" ${cleaning ? 'disabled' : ''}>${cleaning ? 'Limpando…' : 'Limpar lixeira'}</button>`;
    $('clean').onclick = clean;
  } else {
    el.innerHTML = `<span class="ico ok">${icon('check')}</span><span>Nada para limpar agora.</span>`;
  }
}

async function poll() {
  try {
    const r = await fetch('/api/drive');
    if (r.ok) render(await r.json());
  } catch (e) {
    if (D.busy) setTimeout(poll, 3000);
  }
}

async function clean() {
  if (!confirm(`Apagar da lixeira do backup os arquivos com mais de ${cfg.trashAge}?`)) return;
  const before = D.trash, lix = (D.parts || []).findIndex(p => p.name.toLowerCase() === 'lixeira');
  cleaning = cfg.base + '/lixeira'; note = '';
  bar.fizz(lix); render(D);

  let d = D;
  try {
    const r = await fetch(`/jobs/${cfg.trashJob}/run`, { method: 'POST' });
    const run = r.url.match(/\/runs\/(\d+)/);
    let failed = !run;
    if (run) {
      for (;;) {
        await sleep(1500);
        const lr = await fetch(`/runs/${run[1]}/live`);
        if (!lr.ok) continue;
        const t = await lr.text();
        if (!t.includes('data-running="1"')) { failed = t.includes('data-status="error"'); break; }
      }
      await fetch('/drive/refresh?folder=lixeira', { method: 'POST' });
    }
    d = await (await fetch('/api/drive')).json();
    const freed = before - d.trash;
    if (failed) {
      const msg = new URL(r.url).searchParams.get('err') || 'A limpeza falhou.';
      note = `<span class="err">${esc(msg)}</span>` + (run ? ` <a href="/runs/${run[1]}">Ver execução</a>` : '');
    } else if (freed > 0) {
      note = `<span class="ico ok">${icon('check')}</span><b>Liberados ${fmtBytes(freed)}.</b><span class="dim">${fmtBytes(d.total - d.used)} livres</span>`;
    } else {
      note = `<span class="ico ok">${icon('check')}</span><span>Nada tinha mais de ${esc(cfg.trashAge)}.</span>`;
    }
  } catch (e) {
    note = '<span class="err">Não foi possível falar com o servidor.</span>';
  } finally {
    cleaning = ''; bar.fizz(-1);
    render(d);
  }
}

const legend = $('legend');
const pick = e => {
  const li = e.target.closest('li');
  hover = li ? +li.dataset.i : -1;
  legend.querySelectorAll('li').forEach(x => x.classList.toggle('on', +x.dataset.i === hover));
  bar.highlight(hover); sub();
};
legend.addEventListener('mouseover', pick);
legend.addEventListener('focusin', pick);
legend.addEventListener('mouseleave', () => pick({ target: legend }));
legend.addEventListener('focusout', () => pick({ target: legend }));

$('rescan').onclick = async () => {
  note = ''; $('rescan').disabled = true;
  await fetch('/drive/refresh', { method: 'POST' });
  await sleep(300); poll();
};

render(D);

// HDs locais: uma barra por HD, com hover igual ao do Drive.
let diskBars = [];
function renderDisks(ds) {
  const box = $('disks');
  diskBars.forEach(b => b.destroy()); diskBars = [];
  if (!ds || !ds.length) { $('disks-card').hidden = true; return; }
  $('disks-card').hidden = false;
  box.innerHTML = ds.map((d, k) => `
    <div class="disk" data-k="${k}">
      <p class="disk-head"><span class="ico ${d.mounted ? '' : 'bad'}" style="${d.mounted ? 'color:#3b82f6' : ''}">${icon(d.mounted ? 'hdd' : 'x')}</span>
        <b>${esc(d.mount)}</b><span class="dim disk-sub">${d.mounted ? `${fmtBytes(d.used)} de ${fmtBytes(d.total)} usados` : 'Não montado'}</span></p>
      ${d.mounted ? `<canvas class="pixelbar slim"></canvas>
      <ul class="legend">${(d.parts || []).map((p, i) => `<li data-i="${i}" tabindex="0" title="${esc(p.desc || '')}"><span class="swatch" style="--c:${esc(p.color)}"></span><span class="nm">${esc(p.name)}</span><span class="val">${fmtBytes(p.bytes)}</span></li>`).join('')}
        <li class="free"><span class="swatch free"></span><span class="nm dim">Livre</span><span class="val">${fmtBytes(d.total - d.used)}</span></li></ul>` : ''}
      ${d.err ? `<p class="sub err">${esc(d.err)}</p>` : ''}
    </div>`).join('');
  box.querySelectorAll('.disk').forEach(el => {
    const d = ds[+el.dataset.k], c = el.querySelector('canvas');
    if (!c || !d.total) return;
    const b = new PixelBar(c, { top: 20 });
    diskBars.push(b);
    b.set(d.parts || [], d.total);
    const lg = el.querySelector('.legend'), sub = el.querySelector('.disk-sub'), rest = sub.textContent;
    const on = e => {
      const li = e.target.closest('li[data-i]'), i = li ? +li.dataset.i : -1;
      lg.querySelectorAll('li').forEach(x => x.classList.toggle('on', x === li));
      b.highlight(i);
      sub.textContent = i >= 0 ? `${d.parts[i].name}: ${fmtBytes(d.parts[i].bytes)}. ${d.parts[i].desc}` : rest;
    };
    lg.addEventListener('mouseover', on);
    lg.addEventListener('focusin', on);
    lg.addEventListener('mouseleave', () => on({ target: lg }));
    lg.addEventListener('focusout', () => on({ target: lg }));
  });
}
renderDisks(cfg.disks);
async function pollDisks() {
  try {
    if (!D.busy) {
      const r = await fetch('/api/disks');
      if (r.ok) renderDisks(await r.json());
    }
  } catch (e) { /* tenta de novo no próximo ciclo */ }
  setTimeout(pollDisks, 60000);
}
setTimeout(pollDisks, 60000);

// Execução em andamento: card com barra de progresso no topo.
let rbar;
async function pollRunning() {
  let next = 15000;
  try {
    const r = await fetch('/api/running');
    if (r.ok) next = showRunning(await r.json()) ? 2000 : 15000;
  } catch (e) { /* tenta de novo no próximo ciclo */ }
  setTimeout(pollRunning, next);
}

// Mostra o card da execução; devolve true se há execução ativa.
function showRunning(x) {
  const card = $('running');
  if (!x.id) {
    if (!card.hidden) { if (cleaning) card.hidden = true; else location.reload(); }
    return false;
  }
  card.hidden = false;
  rbar ??= new PixelBar($('r-bar'));
  $('r-link').href = '/runs/' + x.id;
  $('r-job').textContent = x.job + (x.dry ? ' (simulação)' : '');
  $('r-file').textContent = x.file || '';
  countTo($('r-bytes'), x.bytes || 0, fmtBytes);
  $('r-of').textContent = x.total ? `de ${fmtBytes(x.total)}${x.speed ? `, ${fmtBytes(x.speed)}/s` : ''}` : 'verificando';
  rbar.set([{ name: 'run', bytes: x.bytes || 0, color: '#3b82f6' }], Math.max(x.total || 0, x.bytes || 0, 1));
  return true;
}
setTimeout(pollRunning, cfg.running ? 0 : 15000);

// Dica do mapa de execuções.
document.querySelector('.heat')?.addEventListener('mouseover', e => {
  const c = e.target.closest('.hc');
  $('heat-tip').textContent = c ? c.dataset.tip : '';
});
