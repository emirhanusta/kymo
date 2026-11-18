import { describe, it, expect } from 'vitest';
import { buildJobRow, jobStatus } from './jobs';
import { Container, CronJobExecution, WorkloadSummary } from '../../core/models/container.model';

const wl = (kind: string) => ({ kind } as WorkloadSummary);
const ex = (over: Partial<CronJobExecution>): CronJobExecution => ({
  started_at: '2026-01-01T00:00:00Z', ...over,
} as CronJobExecution);
const row = (kind: string, executions: CronJobExecution[]) =>
  buildJobRow({ workload: wl(kind), executions, instances: [] });

describe('jobStatus', () => {
  it('no runs', () => {
    expect(jobStatus(row('CronJob', []))).toEqual({ status: 'unknown', text: 'No runs' });
  });
  it('running run', () => {
    expect(jobStatus(row('CronJob', [ex({})]))).toEqual({ status: 'running', text: 'Running' });
  });
  it('last finished run failed', () => {
    const r = row('CronJob', [ex({ completed_at: '2026-01-01T00:01:00Z', exit_code: 1 })]);
    expect(jobStatus(r)).toEqual({ status: 'crashing', text: 'Last run failed' });
  });
  it('last run ok', () => {
    const e = ex({ completed_at: '2026-01-01T00:01:00Z', succeeded: true });
    expect(jobStatus(row('CronJob', [e]))).toEqual({ status: 'stopped', text: 'Idle' });
    const pod = { id: 'ns/pod/c', current_status: 'exited', crash_count: 0, last_seen_at: '2026-01-01T00:01:00Z' } as Container;
    const job = buildJobRow({ workload: wl('Job'), executions: [], instances: [pod] });
    expect(jobStatus(job)).toEqual({ status: 'stopped', text: 'Succeeded' });
  });
});
