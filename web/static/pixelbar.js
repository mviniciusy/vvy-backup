// Barra segmentada em grade de pixels, desenhada em <canvas>.
// bar.set(parts, total) anima até os novos tamanhos; encolher solta partículas.
// bar.highlight(i) apaga os outros segmentos; bar.fizz(i) solta partículas sem parar (limpeza em curso).
class PixelBar {
  constructor(canvas, { cell = 6, dot = 3, top = 28 } = {}) {
    Object.assign(this, { c: canvas, g: canvas.getContext('2d'), cell, dot, top });
    this.parts = []; this.shown = new Map(); this.total = 1; this.hi = -1; this.fizzing = -1;
    this.particles = []; this.tick = 0; this.raf = 0;
    this.ro = new ResizeObserver(() => this.resize());
    this.ro.observe(canvas);
    this.resize();
    this.timer = setInterval(() => {
      if (document.hidden) return;
      this.tick++; if (!this.raf) this.draw();
    }, 400);
  }

  // Libera timer, observer e animação pendente (chamar antes de descartar a barra).
  destroy() {
    clearInterval(this.timer);
    this.ro.disconnect();
    if (this.raf) cancelAnimationFrame(this.raf);
    this.raf = 0;
  }

  resize() {
    const dpr = devicePixelRatio || 1;
    this.w = this.c.clientWidth; this.h = this.c.clientHeight;
    this.c.width = this.w * dpr; this.c.height = this.h * dpr;
    this.g.setTransform(dpr, 0, 0, dpr, 0, 0);
    this.draw();
  }

  set(parts, total) {
    this.parts = parts; this.total = Math.max(total, 1);
    const names = new Set(parts.map(p => p.name));
    for (const k of this.shown.keys()) if (!names.has(k)) this.shown.delete(k);
    this.animate();
  }

  highlight(i) { this.hi = i; this.draw(); }
  fizz(i) { this.fizzing = i; this.animate(); }

  animate() {
    if (this.raf) return;
    const step = () => {
      let moving = false;
      this.parts.forEach((p, i) => {
        const t = p.bytes / this.total, s = this.shown.get(p.name) ?? 0;
        if (Math.abs(t - s) > 1e-5) {
          const n = s + (t - s) * 0.1;
          this.shown.set(p.name, Math.abs(t - n) < 2e-4 ? t : n);
          if (t < s) this.emit(i, 3);
          moving = true;
        }
      });
      if (this.fizzing >= 0) { this.emit(this.fizzing, 2); moving = true; }
      this.particles = this.particles.filter(q => {
        q.x += q.vx; q.y += q.vy; q.vy += 0.02; return --q.life > 0;
      });
      if (this.particles.length) moving = true;
      this.draw();
      this.raf = moving ? requestAnimationFrame(step) : 0;
    };
    this.raf = requestAnimationFrame(step);
  }

  // Largura na tela: proporcional, mas com um mínimo para pastas pequenas não sumirem numa cota grande.
  width(p) {
    const s = this.shown.get(p.name) ?? 0, t = p.bytes / this.total;
    return t > 0 ? Math.max(s * this.w, 10 * Math.min(1, s / t)) : s * this.w;
  }

  edge(i) {
    let x = 0;
    for (let k = 0; k <= i; k++) x += this.width(this.parts[k]);
    return x;
  }

  emit(i, n) {
    const x = this.edge(i), h = this.h - this.top, color = this.parts[i]?.color || '#fff';
    for (let k = 0; k < n; k++) {
      this.particles.push({
        x: x - Math.random() * 10, y: this.top + Math.random() * h,
        vx: Math.random() * 0.9 - 0.2, vy: -0.4 - Math.random() * 1.6,
        life: 30 + Math.random() * 30, max: 60, color: Math.random() < 0.4 ? '#fff' : color,
      });
    }
  }

  static rand(a, b, c) {
    let h = Math.imul(a, 374761393) ^ Math.imul(b, 668265263) ^ Math.imul(c, 2246822519);
    h = Math.imul(h ^ (h >>> 13), 1274126177);
    return ((h ^ (h >>> 16)) >>> 0) / 4294967296;
  }

  draw() {
    const { g, w, h, top, cell, dot } = this, bh = h - top, off = (cell - dot) / 2;
    const cols = Math.floor(w / cell), rows = Math.floor(bh / cell);
    g.clearRect(0, 0, w, h);
    if (!w) return;

    g.save();
    g.beginPath(); g.roundRect(0.5, top + 0.5, w - 1, bh - 1, 10); g.clip();
    g.fillStyle = 'rgba(255,255,255,0.05)';
    for (let cx = 0; cx < cols; cx++) for (let cy = 0; cy < rows; cy++)
      g.fillRect(cx * cell + off, top + cy * cell + off, dot, dot);

    let x = 0;
    this.parts.forEach((p, i) => {
      const sw = this.width(p);
      if (sw < 0.5) { x += sw; return; }
      const a = this.hi >= 0 && this.hi !== i ? 0.28 : 1, lit = this.hi === i;
      g.save();
      g.beginPath(); g.rect(x, top, sw, bh); g.clip();
      g.globalAlpha = a;
      g.fillStyle = p.color + '1f'; g.fillRect(x, top, sw, bh);
      for (let cx = Math.floor(x / cell); cx * cell < x + sw; cx++) {
        for (let cy = 0; cy < rows; cy++) {
          const r = PixelBar.rand(i + 1, cx, cy);
          const tw = PixelBar.rand(cx, cy, Math.floor((this.tick + r * 7) / 3));
          const depth = cy / rows;
          const bright = tw < 0.025 + 0.11 * depth * depth;
          g.fillStyle = bright ? (r < 0.3 ? '#ffffff' : p.color) : p.color + (lit ? '80' : '55');
          g.fillRect(cx * cell + off, top + cy * cell + off, dot, dot);
        }
      }
      g.globalAlpha = a;
      g.shadowColor = p.color; g.shadowBlur = lit ? 16 : 8;
      g.strokeStyle = p.color; g.lineWidth = lit ? 2 : 1.5;
      g.strokeRect(x + 0.75, top + 0.75, sw - 1.5, bh - 1.5);
      g.restore();
      x += sw;
    });
    g.restore();

    g.strokeStyle = 'rgba(255,255,255,0.07)'; g.lineWidth = 1;
    g.beginPath(); g.roundRect(0.5, top + 0.5, w - 1, bh - 1, 10); g.stroke();

    for (const q of this.particles) {
      g.globalAlpha = Math.max(0, q.life / q.max);
      g.fillStyle = q.color;
      g.fillRect(q.x, q.y, 2.5, 2.5);
    }
    g.globalAlpha = 1;
  }
}

// Número que corre até o valor novo.
function countTo(el, to, fmt, ms = 700) {
  const from = +(el.dataset.v || 0), t0 = performance.now();
  cancelAnimationFrame(el._raf);
  el.dataset.v = to;
  const f = now => {
    const k = Math.min(1, (now - t0) / ms), e = 1 - Math.pow(1 - k, 3);
    el.textContent = fmt(from + (to - from) * e);
    if (k < 1) el._raf = requestAnimationFrame(f);
  };
  el._raf = requestAnimationFrame(f);
}

function fmtBytes(n) {
  if (!Number.isFinite(n)) return '—';
  if (n < 0) return '-' + fmtBytes(-n);
  const u = ['B', 'KB', 'MB', 'GB', 'TB'];
  let i = 0;
  while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
  return i ? n.toFixed(1) + ' ' + u[i] : Math.round(n) + ' B';
}
