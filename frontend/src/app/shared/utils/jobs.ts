import { Container, CronJobExecution, WorkloadSummary } from '../../core/models/container.model';
import { normalizeStatus } from './status';
import { podNameFromId } from './source';

export type RunState = 'ok' | 'failed' | 'running';
export type JobHealth = 'failing' | 'flaky' | 'ok' | 'none';

/** One square in the run strip. */
export interface RunCell {
  state: RunState;
  /** epoch ms (0 when unknown) */
  time: number;
  durationSeconds?: number;
  exitCode?: number | null;
  /** ISO timestamp used for "x ago" and as an issue's "since". */
  at?: string;
}

/** Raw data the state service collects per Job/CronJob workload. */
export interface JobInfo {
  workload: WorkloadSummary;
  /** CronJob executions (any order). */
  executions: CronJobExecution[];
  /** Standalone Job: its pods/containers. */
  instances: Container[];
}

export interface JobRow {
  w: WorkloadSummary;
  cells: RunCell[];            // oldest → newest
  last: RunCell | null;        // newest cell
  lastFinished: RunCell | null;
  ok: number;
  failed: number;
  finished: number;
  /** 0–1, or null without finished runs. */
  rate: number | null;
  avgDuration: number | null;
  health: JobHealth;
}

export function executionState(e: CronJobExecution): RunState {
  if (e.succeeded) return 'ok';
  if (e.completed_at || (e.exit_code != null)) return 'failed';
  return e.started_at ? 'running' : 'failed';
}

const ts = (s?: string | null) => (s ? new Date(s).getTime() || 0 : 0);

function executionCell(e: CronJobExecution): RunCell {
  return {
    state: executionState(e),
    time: ts(e.started_at) || ts(e.completed_at) || ts(e.sampled_at),
    at: e.completed_at || e.started_at || e.sampled_at,
    durationSeconds: e.duration_seconds,
    exitCode: e.exit_code ?? null,
  };
}

/** A standalone Job's pods: failed if it crashed, running if up, otherwise it completed. */
function instanceCells(instances: Container[]): RunCell[] {
  const byPod = new Map<string, Container[]>();
  for (const c of instances) {
    const key = podNameFromId(c.id) || c.id;
    (byPod.get(key) ?? byPod.set(key, []).get(key)!).push(c);
  }
  const cells: RunCell[] = [];
  for (const cs of byPod.values()) {
    const failed = cs.some(c => c.crash_count > 0 || normalizeStatus(c.current_status) === 'crashing');
    const running = cs.some(c => normalizeStatus(c.current_status) === 'running');
    cells.push({
      state: failed ? 'failed' : running ? 'running' : 'ok',
      time: Math.max(...cs.map(c => ts(c.last_seen_at))),
      at: cs.map(c => c.last_seen_at).sort().at(-1),
    });
  }
  return cells.sort((a, b) => a.time - b.time);
}

export function buildJobRow(info: JobInfo): JobRow {
  const w = info.workload;
  const cells = (w.kind === 'CronJob'
    ? info.executions.map(executionCell).sort((a, b) => a.time - b.time)
    : instanceCells(info.instances)
  ).slice(-20);

  const done = cells.filter(c => c.state !== 'running');
  const ok = done.filter(c => c.state === 'ok').length;
  const failed = done.length - ok;
  const durs = done.map(c => c.durationSeconds).filter((d): d is number => typeof d === 'number' && d >= 0);
  const lastFinished = done.at(-1) ?? null;
  const rate = done.length ? ok / done.length : null;

  let health: JobHealth = 'none';
  if (lastFinished) {
    if (lastFinished.state === 'failed') health = 'failing';
    else if (rate !== null && rate < 0.8) health = 'flaky';
    else health = 'ok';
  }
  return {
    w, cells, last: cells.at(-1) ?? null, lastFinished, ok, failed, finished: done.length, rate,
    avgDuration: durs.length ? durs.reduce((a, b) => a + b, 0) / durs.length : null,
    health,
  };
}

const HEALTH_RANK: Record<JobHealth, number> = { failing: 0, flaky: 1, none: 2, ok: 3 };
export const healthRank = (h: JobHealth) => HEALTH_RANK[h];

export const HEALTH_DOT: Record<JobHealth, string> = {
  failing: 'bg-kymo-danger', flaky: 'bg-kymo-amber', ok: 'bg-kymo-running', none: 'bg-kymo-unknown',
};
export const RUN_CELL_CLASS: Record<RunState, string> = {
  ok: 'bg-kymo-running', failed: 'bg-kymo-danger', running: 'bg-kymo-amber animate-pulse',
};

/** Header badge for a Job/CronJob, derived from its runs rather than pod status. */
export function jobStatus(row: JobRow): { status: string; text: string } {
  if (row.last?.state === 'running') return { status: 'running', text: 'Running' };
  if (!row.lastFinished) return { status: 'unknown', text: 'No runs' };
  if (row.lastFinished.state === 'failed') return { status: 'crashing', text: 'Last run failed' };
  return { status: 'stopped', text: row.w.kind === 'CronJob' ? 'Idle' : 'Succeeded' };
}
