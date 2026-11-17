import {
  Component, ChangeDetectionStrategy, ElementRef, OnInit, computed, inject, input, output, signal,
} from '@angular/core';
import { takeUntilDestroyed } from '@angular/core/rxjs-interop';
import { HttpErrorResponse } from '@angular/common/http';
import { Subject, of } from 'rxjs';
import { catchError, debounceTime, map, switchMap } from 'rxjs/operators';

import { SourceService } from '../../../core/services/source.service';
import { SourceStateService } from '../../../core/services/source-state.service';
import { ToastService, apiErrorMessage, isForbidden } from '../../../core/services/toast.service';
import {
  KubeContext, KubeContextList, ServerMeta, SourceInput, SourceStatus, SourceTestResult,
} from '../../../core/models/container.model';
import { DialogDirective } from '../../../shared/directives/dialog.directive';

export const CLUSTER_NAME_RE = /^[a-z0-9][a-z0-9_.-]{0,62}$/;
const MAX_KUBECONFIG_BYTES = 1024 * 1024;
const PROD_RE = /prod/i;

/** Internal step ids: 2 = connection, 3 = test & save (shown as 1 and 2). */
type Step = 2 | 3;
type KubeMode = 'path' | 'paste';
type ContextsResult = { ok: true; list: KubeContextList } | { ok: false; error: string } | null;

/**
 * Add / edit cluster wizard (modal):
 *   1. connection  2. test & save.
 * In edit mode the name is locked and an empty kubeconfig textarea keeps the stored kubeconfig.
 */
@Component({
  selector: 'app-cluster-wizard',
  standalone: true,
  imports: [DialogDirective],
  templateUrl: './cluster-wizard.component.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class ClusterWizardComponent implements OnInit {
  /** Cluster being edited; null = create. */
  source = input<SourceStatus | null>(null);
  /** Re-adding a removed cluster: the name is prefilled and locked so its recorded history reconnects. */
  presetName = input('');
  closed = output<void>();
  saved  = output<SourceStatus>();

  private api      = inject(SourceService);
  private sources  = inject(SourceStateService);
  private toast    = inject(ToastService);
  private host     = inject<ElementRef<HTMLElement>>(ElementRef);

  readonly steps: { n: Step; label: string }[] = [
    { n: 2, label: 'Connection' },
    { n: 3, label: 'Test & save' },
  ];

  isEdit = computed(() => !!this.source());
  nameLocked = computed(() => this.isEdit() || !!this.presetName());
  step   = signal<Step>(2);

  // ── form ──────────────────────────────────────────────────────────────────
  name           = signal('');
  nameTouched    = signal(false);
  nameTaken      = signal('');           // set from a 409
  kubeMode       = signal<KubeMode>('path');
  kubeconfigPath = signal('');
  kubeconfigText = signal('');
  fileLabel      = signal('');
  fileError      = signal('');
  context        = signal('');
  pollInterval   = signal('');
  readOnly       = signal(false);
  attempted      = signal(false);       // "Next" pressed with errors → show all errors

  // ── server lookups ────────────────────────────────────────────────────────
  meta          = signal<ServerMeta | null>(null);
  contexts      = signal<KubeContext[]>([]);
  contextsState = signal<'idle' | 'loading' | 'ok' | 'error'>('idle');
  contextsError = signal('');

  testResult = signal<SourceTestResult | null>(null);
  testing    = signal(false);
  testError  = signal('');
  saving     = signal(false);
  saveError  = signal('');

  private contexts$ = new Subject<{ kubeconfig_path?: string; kubeconfig?: string } | null>();
  /** Last context for which read-only was auto-ticked (so unticking sticks). */
  private prodTickedFor = '';

  /** Paste/upload needs KYMO_SECRET_KEY on the server (uploaded kubeconfigs are stored encrypted). */
  pasteDisabled = computed(() => this.meta()?.secret_key_configured === false);
  storedUpload  = computed(() => !!this.source()?.settings?.kubeconfig_uploaded);

  selectedContext = computed(() => this.contexts().find(c => c.name === this.context()) ?? null);

  isProd = computed(() => {
    const c = this.selectedContext();
    return [this.context(), c?.cluster, c?.server].some(v => !!v && PROD_RE.test(v));
  });

  nameError = computed(() => {
    if (this.isEdit()) return '';
    const n = this.name().trim();
    // A removed cluster's historic name is taken as is (it was valid when it was added).
    if (this.presetName()) return this.nameTaken() === n || this.sources.find(n) ? 'A cluster with this name already exists.' : '';
    if (!n) return 'Name is required.';
    if (!CLUSTER_NAME_RE.test(n)) {
      return 'Use lowercase letters, digits, "-", "_" or "."; start with a letter or digit; at most 63 characters.';
    }
    if (this.nameTaken() === n || this.sources.find(n)) return 'A cluster with this name already exists.';
    return '';
  });

  pollError = computed(() => {
    const v = this.pollInterval().trim();
    if (!v) return '';
    return /^\d+$/.test(v) && +v >= 1 && +v <= 86400 ? '' : 'Enter a whole number of seconds (1–86400), or leave empty.';
  });

  kubeError = computed(() => {
    if (this.kubeMode() === 'paste') {
      if (this.pasteDisabled() && !this.storedUpload()) return 'Paste / upload is unavailable on this server.';
      if (!this.kubeconfigText().trim() && !(this.isEdit() && this.storedUpload())) return 'Paste or upload a kubeconfig.';
    }
    return '';
  });

  step2Valid = computed(() => !this.nameError() && !this.pollError() && !this.kubeError());

  canSave = computed(() => !!this.testResult()?.ok && !this.testing() && !this.saving());
  /** "Save anyway": the test ran (or errored) and did not pass — the source keeps retrying in the background. */
  canSaveAnyway = computed(() =>
    !this.testing() && !this.saving() && (this.testResult()?.ok === false || !!this.testError()));

  constructor() {
    this.contexts$.pipe(
      debounceTime(400),
      switchMap(body => {
        if (!body) return of<ContextsResult>(null);
        this.contextsState.set('loading');
        return this.api.contexts(body).pipe(
          map((list): ContextsResult => ({ ok: true, list })),
          catchError((err: unknown) => of<ContextsResult>({ ok: false, error: apiErrorMessage(err, 'Could not read the kubeconfig.') })),
        );
      }),
      takeUntilDestroyed(),
    ).subscribe(res => this.onContexts(res));
  }

  ngOnInit(): void {
    this.api.getMeta().subscribe({
      next: m => {
        this.meta.set(m);
        if (m?.secret_key_configured === false && this.kubeMode() === 'paste' && !this.storedUpload()) {
          this.kubeMode.set('path');
        }
      },
      error: () => this.meta.set(null), // unknown → allow paste; the backend validates on save
    });

    if (this.presetName()) this.name.set(this.presetName());
    const src = this.source();
    if (!src) return;
    const s = src.settings ?? { kubeconfig_uploaded: false };
    this.name.set(src.name);
    this.kubeMode.set(s.kubeconfig_uploaded ? 'paste' : 'path');
    this.kubeconfigPath.set(s.kubeconfig_path ?? '');
    this.context.set(s.kube_context ?? '');
    this.prodTickedFor = s.kube_context ?? '';
    this.pollInterval.set(s.poll_interval_seconds ? String(s.poll_interval_seconds) : '');
    this.readOnly.set(src.read_only);
    this.requestContexts();
  }

  // ── navigation ────────────────────────────────────────────────────────────

  next(): void {
    if (this.step() === 2) {
      this.attempted.set(true);
      this.nameTouched.set(true);
      if (!this.step2Valid()) {
        this.focusFirstInvalid();
        return;
      }
      this.goTo(3);
      this.runTest();
    }
  }

  back(): void {
    if (this.step() === 3) this.goTo(2);
  }

  private goTo(step: Step): void {
    this.step.set(step);
    if (step !== 3) {
      this.testResult.set(null);
      this.testError.set('');
      this.saveError.set('');
    }
    setTimeout(() => this.host.nativeElement.querySelector<HTMLElement>('[data-step-focus]')?.focus());
  }

  private focusFirstInvalid(): void {
    setTimeout(() => this.host.nativeElement.querySelector<HTMLElement>('[aria-invalid="true"]')?.focus());
  }

  // ── connection inputs ─────────────────────────────────────────────────────

  onName(v: string): void {
    this.name.set(v);
    this.nameTaken.set('');
  }

  setKubeMode(m: KubeMode): void {
    if (m === 'paste' && this.pasteDisabled() && !this.storedUpload()) return;
    this.kubeMode.set(m);
    this.requestContexts();
  }

  onPath(v: string): void {
    this.kubeconfigPath.set(v);
    this.requestContexts();
  }

  onKubeconfigText(v: string): void {
    this.kubeconfigText.set(v);
    this.fileLabel.set('');
    this.requestContexts();
  }

  async onFile(ev: Event): Promise<void> {
    const el = ev.target as HTMLInputElement;
    const file = el.files?.[0];
    el.value = ''; // allow re-selecting the same file
    this.fileError.set('');
    if (!file) return;
    if (file.size > MAX_KUBECONFIG_BYTES) {
      this.fileError.set('File is larger than 1 MB — is this really a kubeconfig?');
      return;
    }
    try {
      this.kubeconfigText.set(await file.text());
      this.fileLabel.set(file.name);
      this.requestContexts();
    } catch {
      this.fileError.set('Could not read the file.');
    }
  }

  setContext(name: string): void {
    this.context.set(name);
    this.applyProdDefault();
  }

  private requestContexts(): void {
    if (this.kubeMode() === 'path') {
      const p = this.kubeconfigPath().trim();
      this.contexts$.next(p ? { kubeconfig_path: p } : null);
    } else {
      const t = this.kubeconfigText();
      this.contexts$.next(t.trim() ? { kubeconfig: t } : null);
    }
  }

  private onContexts(res: ContextsResult): void {
    if (!res) {
      this.contexts.set([]);
      this.contextsState.set('idle');
      this.contextsError.set('');
      return;
    }
    if (!res.ok) {
      this.contexts.set([]);
      this.contextsState.set('error');
      this.contextsError.set(res.error);
      return;
    }
    const list = res.list?.contexts ?? [];
    this.contexts.set(list);
    this.contextsState.set('ok');
    this.contextsError.set('');
    if (!list.some(c => c.name === this.context())) {
      this.context.set(list.some(c => c.name === res.list.current) ? res.list.current : (list[0]?.name ?? ''));
    }
    this.applyProdDefault();
  }

  /** Production-looking context → pre-tick read-only once per context (the user may untick it). */
  private applyProdDefault(): void {
    const ctx = this.context();
    if (this.isProd() && ctx !== this.prodTickedFor) {
      this.readOnly.set(true);
    }
    this.prodTickedFor = ctx;
  }

  contextLabel(c: KubeContext): string {
    const where = [c.cluster, c.server].filter(Boolean).join(' · ');
    return where ? `${c.name} — ${where}` : c.name;
  }

  // ── test & save ───────────────────────────────────────────────────────────

  buildInput(): SourceInput {
    const input: SourceInput = {
      name: this.isEdit() ? this.source()!.name : this.name().trim(),
      type: 'kubernetes',
      enabled: this.isEdit() ? this.source()!.enabled : true,
      read_only: this.readOnly(),
    };
    if (this.kubeMode() === 'path') {
      const p = this.kubeconfigPath().trim();
      if (p) input.kubeconfig_path = p;
    } else if (this.kubeconfigText().trim()) {
      input.kubeconfig = this.kubeconfigText();
    }
    const ctx = this.context().trim();
    if (ctx) input.kube_context = ctx;
    const poll = this.pollInterval().trim();
    if (poll) input.poll_interval_seconds = +poll;
    return input;
  }

  runTest(): void {
    this.testing.set(true);
    this.testResult.set(null);
    this.testError.set('');
    this.saveError.set('');
    this.api.test(this.buildInput()).subscribe({
      next: res => {
        this.testResult.set({ ok: !!res?.ok, checks: res?.checks ?? [] });
        this.testing.set(false);
      },
      error: err => {
        this.testing.set(false);
        this.testError.set(apiErrorMessage(err, 'The connection test could not be run.'));
      },
    });
  }

  save(): void {
    if (this.saving()) return;
    this.saving.set(true);
    this.saveError.set('');
    const input = this.buildInput();
    const req = this.isEdit() ? this.api.update(this.source()!.name, input) : this.api.create(input);
    req.subscribe({
      next: saved => {
        this.saving.set(false);
        this.toast.success(this.isEdit() ? `Cluster ${input.name} updated` : `Cluster ${input.name} added — connecting…`);
        this.sources.loadSources();
        this.saved.emit(saved);
      },
      error: (err: HttpErrorResponse) => {
        this.saving.set(false);
        if (isForbidden(err)) return; // toasted by the interceptor
        const msg = apiErrorMessage(err, 'Saving the cluster failed.');
        if (err?.status === 409 && !this.isEdit()) {
          this.nameTaken.set(input.name);
          this.nameTouched.set(true);
          this.goTo(2);
          return;
        }
        this.saveError.set(msg);
      },
    });
  }

  /** Warnings are amber regardless of `ok`; otherwise ✓ green / ✕ red. */
  checkTone(c: { ok: boolean; warning?: boolean }): 'ok' | 'warn' | 'fail' {
    return c.warning ? 'warn' : c.ok ? 'ok' : 'fail';
  }

  checkIcon(c: { ok: boolean; warning?: boolean }): string {
    return { ok: '✓', warn: '!', fail: '✕' }[this.checkTone(c)];
  }

  checkClass(c: { ok: boolean; warning?: boolean }): string {
    return {
      ok: 'text-kymo-running border-kymo-running/40',
      warn: 'text-kymo-amber border-kymo-amber/50',
      fail: 'text-kymo-danger border-kymo-danger/50',
    }[this.checkTone(c)];
  }
}
