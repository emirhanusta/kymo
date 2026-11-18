import { Component, ChangeDetectionStrategy, computed, inject, output, signal } from '@angular/core';
import { FormsModule } from '@angular/forms';

import { ErrorLogService } from '../../../core/services/error-log.service';
import { NamespaceService } from '../../../core/services/namespace.service';
import { SourceStateService } from '../../../core/services/source-state.service';
import { WorkloadStateService } from '../../../core/services/workload-state.service';
import { ToastService } from '../../../core/services/toast.service';
import { DialogDirective } from '../../../shared/directives/dialog.directive';

/** "Add capture target" dialog: cluster, namespace and an optional single workload. */
@Component({
  selector: 'app-add-target-dialog',
  standalone: true,
  imports: [FormsModule, DialogDirective],
  template: `
<div class="fixed inset-0 bg-kymo-deep/85 z-50 flex items-center justify-center p-4 overflow-y-auto" (click)="closed.emit()">
  <form appDialog (dismiss)="closed.emit()" aria-labelledby="add-target-title" (ngSubmit)="submit()"
        class="bg-kymo-panel border border-kymo-rule border-t-kymo-amber p-5 max-w-md w-full space-y-4" (click)="$event.stopPropagation()">
    <div>
      <h3 id="add-target-title" class="text-sm font-semibold uppercase tracking-[0.08em] text-kymo-ink">Add capture target</h3>
      <p class="text-xs text-kymo-muted mt-1.5">Error-level log lines of every running container in the namespace (or workload) are recorded.</p>
    </div>
    <label class="flex flex-col gap-1">
      <span class="k-label">Cluster</span>
      <select class="k-input" name="source" data-autofocus required [ngModel]="source()" (ngModelChange)="onSource($event)">
        <option value="" disabled>Select…</option>
        @for (s of clusters(); track s.name) { <option [value]="s.name">{{ s.name }}</option> }
      </select>
    </label>
    <label class="flex flex-col gap-1">
      <span class="k-label">Namespace</span>
      <input class="k-input" name="ns" list="lc-namespaces" autocomplete="off" placeholder="e.g. shop" required
             [ngModel]="ns()" (ngModelChange)="ns.set($event); workload.set('')" />
      <datalist id="lc-namespaces">
        @for (n of namespaces(); track n) { <option [value]="n"></option> }
      </datalist>
    </label>
    <label class="flex flex-col gap-1">
      <span class="k-label">Workload <span class="normal-case tracking-normal text-kymo-faint">· optional</span></span>
      <select class="k-input" name="wl" [ngModel]="workload()" (ngModelChange)="workload.set($event)">
        <option value="">Whole namespace</option>
        @for (w of workloadOptions(); track w.id) { <option [value]="w.id">{{ w.kind }}/{{ w.name }}</option> }
      </select>
    </label>
    <div class="flex justify-end gap-2 pt-1">
      <button type="button" class="k-btn" (click)="closed.emit()">Cancel</button>
      <button type="submit" class="k-btn k-btn-primary" [disabled]="!canAdd() || adding()">{{ adding() ? 'Adding…' : 'Add target' }}</button>
    </div>
  </form>
</div>
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class AddTargetDialogComponent {
  private api       = inject(ErrorLogService);
  private nsSvc     = inject(NamespaceService);
  private sourceSvc = inject(SourceStateService);
  private wlState   = inject(WorkloadStateService);
  private toast     = inject(ToastService);

  saved  = output<void>();
  closed = output<void>();

  source     = signal('');
  ns         = signal('');
  workload   = signal('');
  namespaces = signal<string[]>([]);
  adding     = signal(false);

  clusters = computed(() => this.sourceSvc.sources());
  canAdd = computed(() => !!this.source() && !!this.ns().trim());
  workloadOptions = computed(() => {
    const src = this.source(), ns = this.ns().trim();
    if (!src || !ns) return [];
    return this.wlState.workloads().filter(w => w.source_name === src && w.namespace === ns && !w.is_system)
      .sort((a, b) => a.kind.localeCompare(b.kind) || a.name.localeCompare(b.name));
  });

  onSource(name: string): void {
    this.source.set(name);
    this.workload.set('');
    this.namespaces.set([]);
    if (name) this.nsSvc.getNamespaces(name).subscribe(ns => this.namespaces.set(ns ?? []));
  }

  submit(): void {
    if (!this.canAdd() || this.adding()) return;
    this.adding.set(true);
    const workload = this.workload();
    this.api.addTarget({
      source_name: this.source(), namespace: this.ns().trim(),
      ...(workload ? { workload_id: workload } : {}),
    }).subscribe({
      next: () => { this.adding.set(false); this.toast.success('Capture target added'); this.saved.emit(); },
      error: err => {
        this.adding.set(false);
        if (err?.status === 409) this.toast.error('That target already exists');
        else this.toast.apiError(err, 'Failed to add target');
      },
    });
  }
}
