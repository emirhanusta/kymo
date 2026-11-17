import { Component, ChangeDetectionStrategy, signal, computed, inject } from '@angular/core';

import { NotificationService, channelToInput } from '../../../core/services/notification.service';
import { SourceService } from '../../../core/services/source.service';
import { ToastService } from '../../../core/services/toast.service';
import { RefreshService } from '../../../core/services/refresh.service';
import { ServerMeta } from '../../../core/models/container.model';
import { NotificationChannel, NotificationDelivery } from '../../../core/models/issue.model';
import { reloadOnSourceAndRefresh } from '../../../core/utils/page-reload';
import { PageHeaderComponent } from '../../../shared/components/page-header/page-header.component';
import { EmptyStateComponent } from '../../../shared/components/empty-state/empty-state.component';
import { ErrorStateComponent } from '../../../shared/components/error-state/error-state.component';
import { SkeletonComponent } from '../../../shared/components/skeleton/skeleton.component';
import { ConfirmDialogComponent } from '../../../shared/components/confirm-dialog/confirm-dialog.component';
import { TimeagoPipe } from '../../../shared/pipes/timeago.pipe';
import { CONFIG_ORIGIN_HINT } from '../../../shared/utils/source';
import { ChannelDialogComponent } from './channel-dialog/channel-dialog.component';
import { channelFilterSummary, channelTestMessage } from './channel-utils';

const TYPE_LABEL = { slack: 'Slack', discord: 'Discord', webhook: 'Webhook' } as const;

/** Settings → Notifications: where issue alerts go (Slack, Discord, generic webhooks) and what was delivered. */
@Component({
  selector: 'app-notifications',
  standalone: true,
  imports: [
    TimeagoPipe, PageHeaderComponent, EmptyStateComponent, ErrorStateComponent, SkeletonComponent,
    ConfirmDialogComponent, ChannelDialogComponent,
  ],
  templateUrl: './notifications.component.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class NotificationsComponent {
  private api     = inject(NotificationService);
  private sources = inject(SourceService);
  private toast   = inject(ToastService);
  private refresh = inject(RefreshService);

  readonly typeLabel = TYPE_LABEL;
  readonly summary   = channelFilterSummary;
  readonly configHint = CONFIG_ORIGIN_HINT;

  meta       = signal<ServerMeta | null>(null);
  channels   = signal<NotificationChannel[]>([]);
  loaded     = signal(false);
  error      = signal(false);
  /** null = closed; 'new' = create; channel = edit. */
  dialog     = signal<'new' | NotificationChannel | null>(null);
  deleteTarget = signal<NotificationChannel | null>(null);
  busyId     = signal<number | null>(null);
  testingId  = signal<number | null>(null);

  deliveries      = signal<NotificationDelivery[]>([]);
  deliveriesLoaded = signal(false);
  deliveriesError = signal(false);
  filterChannel   = signal<number | null>(null);

  /** Channels need KYMO_SECRET_KEY (webhook URLs are stored encrypted). */
  canAdd = computed(() => this.meta()?.secret_key_configured !== false);
  noPublicUrl = computed(() => this.meta() !== null && !this.meta()!.public_url);
  noSecretKey = computed(() => this.meta() !== null && !this.meta()!.secret_key_configured);

  editChannel = computed(() => {
    const d = this.dialog();
    return d && d !== 'new' ? d : null;
  });

  sorted = computed(() =>
    [...this.channels()].sort((a, b) =>
      (a.origin === b.origin ? 0 : a.origin === 'config' ? -1 : 1) || a.name.localeCompare(b.name)));

  channelName = computed(() => new Map(this.channels().map(c => [c.id, c.name])));

  constructor() {
    reloadOnSourceAndRefresh({ ignoreSource: true, load: () => this.load() });
  }

  load(): void {
    this.sources.getMeta().subscribe({ next: m => this.meta.set(m), error: () => {} });
    this.api.channels().subscribe({
      next: list => {
        this.channels.set(list);
        this.error.set(false);
        this.loaded.set(true);
        this.refresh.markUpdated();
      },
      error: () => { this.error.set(true); this.loaded.set(true); },
    });
    this.loadDeliveries();
  }

  loadDeliveries(): void {
    this.api.deliveries(this.filterChannel()).subscribe({
      next: list => { this.deliveries.set(list); this.deliveriesError.set(false); this.deliveriesLoaded.set(true); },
      error: () => { this.deliveriesError.set(true); this.deliveriesLoaded.set(true); },
    });
  }

  setFilter(value: string): void {
    this.filterChannel.set(value ? +value : null);
    this.deliveriesLoaded.set(false);
    this.loadDeliveries();
  }

  onSaved(): void { this.load(); }

  toggleEnabled(c: NotificationChannel): void {
    if (c.origin === 'config' || this.busyId() !== null) return;
    this.busyId.set(c.id);
    this.api.update(c.id, channelToInput(c, { enabled: !c.enabled })).subscribe({
      next: () => { this.busyId.set(null); this.toast.success(`${c.name} ${c.enabled ? 'disabled' : 'enabled'}`); this.load(); },
      error: err => { this.busyId.set(null); this.toast.apiError(err, `Failed to update ${c.name}`); },
    });
  }

  test(c: NotificationChannel): void {
    if (this.testingId() !== null) return;
    this.testingId.set(c.id);
    this.api.test(c.id).subscribe({
      next: r => {
        this.testingId.set(null);
        (r.ok ? this.toast.success : this.toast.error).call(this.toast, channelTestMessage(c.name, r));
        this.load();
      },
      error: err => { this.testingId.set(null); this.toast.apiError(err, `Test to ${c.name} failed`); },
    });
  }

  confirmDelete(): void {
    const c = this.deleteTarget();
    if (!c) return;
    this.deleteTarget.set(null);
    this.busyId.set(c.id);
    this.api.delete(c.id).subscribe({
      next: () => { this.busyId.set(null); this.toast.success(`Channel ${c.name} deleted`); this.load(); },
      error: err => { this.busyId.set(null); this.toast.apiError(err, `Failed to delete ${c.name}`); },
    });
  }
}
