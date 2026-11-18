/** One source of truth for stability-score colors (0–100). */
export type ScoreTone = 'good' | 'warn' | 'bad' | 'none';

export function scoreTone(score: number | null | undefined): ScoreTone {
  if (score === null || score === undefined || isNaN(score)) return 'none';
  if (score >= 80) return 'good';
  if (score >= 50) return 'warn';
  return 'bad';
}

const TEXT: Record<ScoreTone, string> = {
  good: 'text-kymo-running', warn: 'text-kymo-amber', bad: 'text-kymo-danger', none: 'text-kymo-faint',
};
const BG: Record<ScoreTone, string> = {
  good: 'bg-kymo-running', warn: 'bg-kymo-amber', bad: 'bg-kymo-danger', none: 'bg-kymo-rule',
};
/** CSS variable references (theme-aware) for SVG presentation attributes. Not valid for canvas. */
const HEX: Record<ScoreTone, string> = {
  good: 'var(--color-kymo-running)', warn: 'var(--color-kymo-amber)',
  bad: 'var(--color-kymo-danger)', none: 'var(--color-kymo-faint)',
};

export const scoreTextClass = (s: number | null | undefined) => TEXT[scoreTone(s)];
export const scoreBgClass   = (s: number | null | undefined) => BG[scoreTone(s)];
export const scoreHex       = (s: number | null | undefined) => HEX[scoreTone(s)];

export function formatScore(s: number | null | undefined): string {
  return scoreTone(s) === 'none' ? '—' : String(Math.round(s as number));
}

/** Average of non-null scores, or null when none are available. */
export function averageScore(scores: (number | null | undefined)[]): number | null {
  const vals = scores.filter((s): s is number => typeof s === 'number' && !isNaN(s));
  if (!vals.length) return null;
  return Math.round(vals.reduce((a, b) => a + b, 0) / vals.length);
}
