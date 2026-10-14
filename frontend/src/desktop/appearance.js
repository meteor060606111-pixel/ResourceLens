export const appearanceKey = 'resourcelens.desktop-appearance';
export function normalizeAppearance(value) {
  const source = value && typeof value === 'object' ? value : {};
  return {tint: typeof source.tint === 'number' && Number.isFinite(source.tint) ? Math.round(Math.min(100, Math.max(0, source.tint))) : 50,
    reduceTransparency: source.reduceTransparency === true, reduceMotion: source.reduceMotion === true};
}
export function readAppearance(storage) {
  try { return normalizeAppearance(JSON.parse(storage.getItem(appearanceKey))); }
  catch { return normalizeAppearance(null); }
}
