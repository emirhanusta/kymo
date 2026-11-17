import { Component, ChangeDetectionStrategy, OnInit, WritableSignal, computed, inject, input, output, signal } from '@angular/core';

import { NotificationService } from '../../../../core/services/notification.service';
import { SourceStateService } from '../../../../core/services/source-state.service';
import { ToastService, apiErrorMessage } from '../../../../core/services/toast.service';
import {
  ChannelInput, ChannelType, IssueSeverity, IssueType, NotificationChannel,
} from '../../../../core/models/issue.model';
import { DialogDirective } from '../../../../shared/directives/dialog.directive';
import { ISSUE_TYPES } from '../../../../shared/utils/issues';
import { channelTestMessage } from '../channel-utils';

export const CHANNEL_TYPES: { type: ChannelType; label: string; help: string; placeholder: string }[] = [
  { type: 'slack', label: 'Slack',
    help: 'Create an Incoming Webhook in your Slack app (api.slack.com/apps → Incoming Webhooks) and paste its URL.',
    placeholder: 'https://hooks.slack.com/services/…' },
  { type: 'discord', label: 'Discord',
    help: 'In the channel settings open Integrations → Webhooks → New Webhook and copy the webhook URL.',
    placeholder: 'https://discord.com/api/webhooks/…' },
  { type: 'webhook', label: 'Webhook',
    help: 'Any HTTPS endpoint. KYMO POSTs a JSON payload for every issue event.',
    placeholder: 'https://example.com/hooks/kymo' },
];

export const REPEAT_OPTIONS: { minutes: number; label: string }[] = [
  { minutes: 0, label: 'Off' },
  { minutes: 30, label: 'Every 30 minutes' },
  { minutes: 60, label: 'Every hour' },
  { minutes: 240, label: 'Every 4 hours' },
  { minutes: 1440, label: 'Every 24 hours' },
];

/** Add / edit a notification channel (modal). After saving it offers to send a test message. */
@Component({
  selector: 'app-channel-dialog',
  standalone: true,
  imports: [DialogDirective],
  templateUrl: './channel-dialog.component.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class ChannelDialogComponent implements OnInit {
  /** Channel being edited; null = create. */
  channel = input<NotificationChannel | null>(null);
  closed  = output<void>();
  /** Fired after every successful save (the parent refreshes its list); the dialog stays open for "Send test". */
  saved   = output<NotificationChannel>();

  private api   = inject(NotificationService);
  private toast = inject(ToastService);
  private src   = inject(SourceStateService);

  readonly channelTypes = CHANNEL_TYPES;
  readonly issueTypes   = ISSUE_TYPES;

  isEdit = computed(() => !!this.channel());

  type          = signal<ChannelType>('slack');
  name          = signal('');
  url           = signal('');
  secret        = signal('');
  minSeverity   = signal<IssueSeverity>('critical');
  sources       = signal<string[]>([]);
  namespaces    = signal<string[]>([]);
  nsDraft       = signal('');
  types         = signal<IssueType[]>([]);
  notifyResolved = signal(true);
  includeLogs   = signal(true);
  repeat        = signal(0);
  attempted     = signal(false);

  saving     = signal(false);
  saveError  = signal('');
  /** Set after a successful save: switches the footer to "Send test" / "Done". */
  savedChannel = signal<NotificationChannel | null>(null);
  testing    = signal(false);
  testOutcome = signal<{ ok: boolean; message: string } | null>(null);

  typeInfo = computed(() => CHANNEL_TYPES.find(t => t.type === this.type())!);

  /** Registered clusters, plus any the channel already filters on that no longer exist. */
  clusterOptions = computed(() => {
    const names = new Set(this.src.sources().map(s => s.name));
    for (const s of this.channel()?.sources ?? []) names.add(s);
    return [...names].sort();
  });

  repeatOptions = computed(() => {
    const cur = this.repeat();
    return REPEAT_OPTIONS.some(o => o.minutes === cur) ? REPEAT_OPTIONS : [...REPEAT_OPTIONS, { minutes: cur, label: `Every ${cur} minutes` }];
  });

  nameError = computed(() => (this.name().trim() ? '' : 'Name is required.'));
  urlError  = computed(() => {
    if (this.savedChannel()) return '';
    if (this.isEdit() || this.url().trim()) {
      const u = this.url().trim();
      return u && !/^https?:\/\//i.test(u) ? 'The URL must start with http:// or https://.' : '';
    }
    return 'Webhook URL is required.';
  });

  /** Server-side 400 message, attached to the field it talks about. */
  fieldError = (field: 'url' | 'name' | 'secret'): string => {
    const e = this.saveError();
    return e && new RegExp(`\\b${field}\\b`, 'i').test(e) ? e : '';
  };
  generalError = computed(() => {
    const e = this.saveError();
    return e && !(['url', 'name', 'secret'] as const).some(f => this.fieldError(f)) ? e : '';
  });

  /** Seeds the form once; the edited channel is static for the lifetime of the dialog. */
  ngOnInit(): void {
    const c = this.channel();
    if (!c) return;
    this.type.set(c.type);
    this.name.set(c.name);
    this.minSeverity.set(c.min_severity);
    this.sources.set([...(c.sources ?? [])]);
    this.namespaces.set([...(c.namespaces ?? [])]);
    this.types.set([...(c.types ?? [])]);
    this.notifyResolved.set(c.notify_resolved);
    this.includeLogs.set(c.include_logs);
    this.repeat.set(c.repeat_minutes);
  }

  toggle<T>(sig: WritableSignal<T[]>, value: T, on: boolean): void {
    sig.update(list => on ? (list.includes(value) ? list : [...list, value]) : list.filter(x => x !== value));
  }

  addNamespaces(raw: string): void {
    const parts = raw.split(/[\s,]+/).map(s => s.trim()).filter(Boolean);
    if (parts.length) this.namespaces.update(cur => [...cur, ...parts.filter(p => !cur.includes(p))]);
    this.nsDraft.set('');
  }

  onNsKey(ev: KeyboardEvent): void {
    const input = ev.target as HTMLInputElement;
    if (ev.key === 'Enter' || ev.key === ',') {
      ev.preventDefault();
      this.addNamespaces(input.value);
      input.value = '';
    } else if (ev.key === 'Backspace' && !input.value && this.namespaces().length) {
      this.namespaces.update(l => l.slice(0, -1));
    }
  }

  removeNamespace(ns: string): void {
    this.namespaces.update(l => l.filter(x => x !== ns));
  }

  save(): void {
    this.attempted.set(true);
    this.saveError.set('');
    if (this.saving() || this.nameError() || this.urlError()) return;
    const body: ChannelInput = {
      name: this.name().trim(), type: this.type(), enabled: this.channel()?.enabled ?? true,
      min_severity: this.minSeverity(), sources: this.sources(), namespaces: this.namespaces(), types: this.types(),
      notify_resolved: this.notifyResolved(), include_logs: this.includeLogs(), repeat_minutes: this.repeat(),
    };
    if (this.url().trim()) body.url = this.url().trim();
    if (this.type() === 'webhook' && this.secret().trim()) body.secret = this.secret().trim();

    this.saving.set(true);
    const c = this.channel();
    (c ? this.api.update(c.id, body) : this.api.create(body)).subscribe({
      next: saved => {
        this.saving.set(false);
        this.savedChannel.set(saved);
        this.url.set('');
        this.secret.set('');
        this.toast.success(`Channel ${saved.name} saved`);
        this.saved.emit(saved);
        setTimeout(() => document.getElementById('ch-send-test')?.focus(), 0);
      },
      error: err => {
        this.saving.set(false);
        if (err?.status === 403) return;
        this.saveError.set(apiErrorMessage(err, 'Failed to save the channel'));
      },
    });
  }

  sendTest(): void {
    const c = this.savedChannel();
    if (!c || this.testing()) return;
    this.testing.set(true);
    this.testOutcome.set(null);
    this.api.test(c.id).subscribe({
      next: r => {
        this.testing.set(false);
        this.testOutcome.set({ ok: r.ok, message: channelTestMessage(c.name, r) });
      },
      error: err => {
        this.testing.set(false);
        this.testOutcome.set({ ok: false, message: apiErrorMessage(err, 'The test request failed') });
      },
    });
  }
}
