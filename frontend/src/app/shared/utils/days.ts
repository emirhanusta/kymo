/** Local calendar-day key of a timestamp ("2026-10-18" style, month 0-based; only for grouping). */
export function dayKey(iso: string): string {
  const d = new Date(iso);
  return `${d.getFullYear()}-${d.getMonth()}-${d.getDate()}`;
}

/** "Today · Wed, Nov 18" / "Yesterday · Tue, Nov 17" / "Mon, Nov 16". */
export function dayLabel(iso: string, now = new Date()): string {
  const d = new Date(iso);
  const startOf = (x: Date) => new Date(x.getFullYear(), x.getMonth(), x.getDate()).getTime();
  const diff = Math.round((startOf(now) - startOf(d)) / 86_400_000);
  const full = d.toLocaleDateString('en-US', {
    weekday: 'short', month: 'short', day: 'numeric', ...(d.getFullYear() !== now.getFullYear() ? { year: 'numeric' } : {}),
  });
  return diff === 0 ? `Today · ${full}` : diff === 1 ? `Yesterday · ${full}` : full;
}

export interface DayGroup<T> { key: string; label: string; rows: T[]; }

/** Groups an already sorted list into consecutive local days. */
export function groupByDay<T>(list: T[], at: (x: T) => string): DayGroup<T>[] {
  const out: DayGroup<T>[] = [];
  for (const x of list) {
    const iso = at(x);
    const key = dayKey(iso);
    let g = out[out.length - 1];
    if (!g || g.key !== key) { g = { key, label: dayLabel(iso), rows: [] }; out.push(g); }
    g.rows.push(x);
  }
  return out;
}
