// Reflexo de vidro: guarda a posição do mouse dentro do controle em --mx/--my.
document.addEventListener('pointermove', e => {
  const el = e.target.closest?.('.link, .btn, .side-link');
  if (!el) return;
  const r = el.getBoundingClientRect();
  el.style.setProperty('--mx', `${e.clientX - r.left}px`);
  el.style.setProperty('--my', `${e.clientY - r.top}px`);
}, { passive: true });
