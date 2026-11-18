/** Lookup key of a CronJob across clusters. */
export const cronKey = (source: string, namespace: string | undefined, name: string) => `${source}\u0000${namespace ?? ''}\u0000${name}`;

const DOW = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];
const NAMED: Record<string, string> = {
  '@yearly': 'yearly', '@annually': 'yearly', '@monthly': 'monthly', '@weekly': 'weekly',
  '@daily': 'daily at 00:00', '@midnight': 'daily at 00:00', '@hourly': 'every hour',
};

const isNum = (s: string) => /^\d+$/.test(s);
const pad = (n: string) => n.padStart(2, '0');

/** A plain-language hint for the common cron patterns ("every 5 min", "daily at 02:30"); '' for anything else. */
export function describeSchedule(expr: string): string {
  const s = (expr ?? '').trim().replace(/^(CRON_)?TZ=\S+\s+/, '');
  if (!s) return '';
  if (s.startsWith('@')) {
    const every = /^@every\s+(.+)$/.exec(s);
    return every ? `every ${every[1]}` : NAMED[s] ?? '';
  }
  const f = s.split(/\s+/);
  if (f.length !== 5) return '';
  const [min, hour, dom, mon, dow] = f;
  const step = (v: string) => /^\*\/(\d+)$/.exec(v)?.[1];

  if (mon !== '*') return '';
  if (dom === '*' && dow === '*') {
    if (min === '*' && hour === '*') return 'every minute';
    const m = step(min);
    if (m && hour === '*') return `every ${m} min`;
    if (isNum(min) && hour === '*') return min === '0' ? 'every hour' : `hourly at :${pad(min)}`;
    const h = step(hour);
    if (isNum(min) && h) return `every ${h} h`;
    if (isNum(min) && isNum(hour)) return `daily at ${pad(hour)}:${pad(min)}`;
    return '';
  }
  if (isNum(min) && isNum(hour)) {
    const at = `${pad(hour)}:${pad(min)}`;
    if (dom === '*' && isNum(dow)) return `${DOW[Number(dow) % 7]} at ${at}`;
    if (dom === '*' && dow === '1-5') return `weekdays at ${at}`;
    if (isNum(dom) && dow === '*') return `monthly on day ${dom} at ${at}`;
  }
  return '';
}

/** Future instant → "in 12m", "in 3h", "in 2d"; past or missing → '—'. */
export function untilLabel(iso: string | null | undefined, now = Date.now()): string {
  if (!iso) return '—';
  const ms = new Date(iso).getTime() - now;
  if (isNaN(ms)) return '—';
  if (ms <= 0) return 'due';
  const s = Math.floor(ms / 1000);
  if (s < 60) return `in ${s}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `in ${m}m`;
  const h = Math.floor(m / 60);
  if (h < 24) return m % 60 && h < 6 ? `in ${h}h ${m % 60}m` : `in ${h}h`;
  return `in ${Math.floor(h / 24)}d`;
}
