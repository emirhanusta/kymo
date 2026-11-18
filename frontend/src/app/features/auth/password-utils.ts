export const MIN_PASSWORD = 12;

/** Rough strength hint (length + character classes); the server only enforces the minimum length. */
export function passwordHint(pw: string): { label: string; tone: 'danger' | 'amber' | 'ok' } | null {
  if (!pw) return null;
  if (pw.length < MIN_PASSWORD) return { label: `${MIN_PASSWORD - pw.length} more character${MIN_PASSWORD - pw.length === 1 ? '' : 's'} needed`, tone: 'danger' };
  const classes = [/[a-z]/, /[A-Z]/, /\d/, /[^A-Za-z0-9]/].filter(r => r.test(pw)).length;
  if (pw.length >= 16 || classes >= 3) return { label: 'Strong', tone: 'ok' };
  return { label: 'OK — longer or mixed characters are stronger', tone: 'amber' };
}

export const hintClass = (tone: 'danger' | 'amber' | 'ok') =>
  tone === 'danger' ? 'text-kymo-danger' : tone === 'amber' ? 'text-kymo-amber' : 'text-kymo-running';
