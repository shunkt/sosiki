// formatWeight always shows a sign so a negative interest (−) reads as
// "avoids this topic" without relying on colour alone. Kept in its own
// module (not PersonaCard.tsx) so that file only exports components —
// mixing exports there breaks Vite's fast-refresh boundary.
export function formatWeight(weight: number): string {
  const abs = Math.abs(weight).toFixed(2)
  return weight < 0 ? `−${abs}` : `+${abs}`
}
