// Readable tooltips (SPEC §8.1): one bubble for the whole application, shown
// at once on hover or keyboard focus, in a normal font size and on several
// lines. Usage: <span use:tip={text}>…</span>. The native title attribute
// is small, slow to appear and cannot be styled.

let bubble: HTMLDivElement | null = null;
let owner: Element | null = null;

function ensure(): HTMLDivElement {
  if (!bubble) {
    bubble = document.createElement('div');
    bubble.className = 'tip-bubble';
    bubble.setAttribute('role', 'tooltip');
    bubble.id = 'tip-bubble';
    document.body.appendChild(bubble);
    addEventListener('scroll', hide, true);
    addEventListener('keydown', (e) => e.key === 'Escape' && hide());
  }
  return bubble;
}

function hide() {
  if (bubble) bubble.style.display = 'none';
  owner = null;
}

function show(node: Element, text: string) {
  const b = ensure();
  b.textContent = text;
  b.style.display = 'block';
  owner = node;
  // Below the element, centred; above when there is no room; kept on screen.
  const r = node.getBoundingClientRect();
  const w = b.offsetWidth;
  const h = b.offsetHeight;
  const gap = 8;
  let left = r.left + r.width / 2 - w / 2;
  left = Math.max(gap, Math.min(left, innerWidth - w - gap));
  let top = r.bottom + gap;
  if (top + h > innerHeight - gap && r.top - h - gap > 0) top = r.top - h - gap;
  b.style.left = `${left}px`;
  b.style.top = `${top}px`;
}

/** Svelte action: shows text in the tooltip bubble on hover and focus. */
export function tip(node: Element, text: string | undefined | null) {
  let current = text ?? '';
  const enter = () => current && show(node, current);
  const leave = () => owner === node && hide();
  node.addEventListener('mouseenter', enter);
  node.addEventListener('mouseleave', leave);
  node.addEventListener('focus', enter);
  node.addEventListener('blur', leave);
  const describe = () => {
    if (current) node.setAttribute('aria-description', current);
    else node.removeAttribute('aria-description');
  };
  describe();
  return {
    update(t: string | undefined | null) {
      current = t ?? '';
      describe();
      if (owner === node) {
        if (current) show(node, current);
        else hide();
      }
    },
    destroy() {
      node.removeEventListener('mouseenter', enter);
      node.removeEventListener('mouseleave', leave);
      node.removeEventListener('focus', enter);
      node.removeEventListener('blur', leave);
      if (owner === node) hide();
    },
  };
}
