import {computed, onMounted, onUnmounted, ref, watch} from 'vue';
import {appearanceKey, normalizeAppearance, readAppearance} from './appearance.js';

export function useGlass() {
  let initial;
  try { initial = readAppearance(window.localStorage); } catch { initial = normalizeAppearance(null); }
  const appearance = ref(initial), systemMotion = ref(false), systemTransparency = ref(false), background = ref(false);
  const reducedMotion = computed(() => appearance.value.reduceMotion || systemMotion.value);
  const reducedTransparency = computed(() => appearance.value.reduceTransparency || systemTransparency.value);
  const glassStyle = computed(() => ({'--glass-rail': .10 + appearance.value.tint * .0045,
    '--glass-main': .08 + appearance.value.tint * .004, '--glass-content': .48 + appearance.value.tint * .004}));
  watch(appearance, value => { try { localStorage.setItem(appearanceKey, JSON.stringify(normalizeAppearance(value))); } catch {} }, {deep: true});
  let frame = 0, target = null, pending = null;
  const cleanups = [];
  function clearHighlight() {
    cancelAnimationFrame(frame); frame = 0; pending = null;
    if (target) { target.style.removeProperty('--pointer-x'); target.style.removeProperty('--pointer-y'); target.removeAttribute('data-glass-hover'); target = null; }
  }
  function pointerMove(event) {
    if (reducedMotion.value || background.value || event.pointerType !== 'mouse') { clearHighlight(); return; }
    const next = event.target.closest('button:not(:disabled)');
    if (next !== target) { clearHighlight(); target = next; }
    if (!target) return;
    pending = {x: event.clientX, y: event.clientY};
    if (frame) return;
    frame = requestAnimationFrame(() => {
      frame = 0;
      if (!target || !pending) return;
      const rect = target.getBoundingClientRect();
      target.style.setProperty('--pointer-x', `${pending.x - rect.left}px`);
      target.style.setProperty('--pointer-y', `${pending.y - rect.top}px`);
      target.setAttribute('data-glass-hover', '');
    });
  }
  watch(reducedMotion, value => { if (value) clearHighlight(); });
  onMounted(() => {
    for (const [query, state] of [['(prefers-reduced-motion: reduce)', systemMotion], ['(prefers-reduced-transparency: reduce)', systemTransparency]]) {
      const media = window.matchMedia(query), update = () => { state.value = media.matches; };
      update(); media.addEventListener('change', update); cleanups.push(() => media.removeEventListener('change', update));
    }
    const visibility = () => { background.value = document.hidden; if (document.hidden) clearHighlight(); };
    visibility(); document.addEventListener('visibilitychange', visibility);
    window.addEventListener('blur', clearHighlight);
    cleanups.push(() => document.removeEventListener('visibilitychange', visibility), () => window.removeEventListener('blur', clearHighlight));
  });
  onUnmounted(() => { clearHighlight(); cleanups.forEach(fn => fn()); });
  return {appearance, systemMotion, systemTransparency, reducedMotion, reducedTransparency, background, glassStyle, pointerMove, clearHighlight,
    resetAppearance: () => { appearance.value = normalizeAppearance(null); }};
}
