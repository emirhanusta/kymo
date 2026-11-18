import { Component, ChangeDetectionStrategy, inject, input, output, signal, computed } from '@angular/core';
import { FormsModule } from '@angular/forms';

import { AuthService } from '../../../core/services/auth.service';
import { ToastService } from '../../../core/services/toast.service';
import { AddUserMethod, AddUserResult, Role, USERNAME_RE } from '../../../core/models/auth.model';
import { DialogDirective } from '../../../shared/directives/dialog.directive';

const MIN_PASSWORD = 12;
const ALPHABET = 'abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789';

/** Crypto-random password without look-alike characters. */
function generatePassword(n = 20): string {
  const out: string[] = [];
  const limit = 256 - (256 % ALPHABET.length);
  while (out.length < n) {
    for (const b of crypto.getRandomValues(new Uint8Array(n * 2))) {
      if (b < limit && out.length < n) out.push(ALPHABET[b % ALPHABET.length]);
    }
  }
  return out.join('');
}

/** "Add user" dialog: setup link (default) or temporary password. */
@Component({
  selector: 'app-add-user-dialog',
  standalone: true,
  imports: [FormsModule, DialogDirective],
  template: `
<div class="fixed inset-0 bg-kymo-deep/85 z-50 flex items-center justify-center p-4 overflow-y-auto" (click)="closed.emit()">
  <form appDialog (dismiss)="closed.emit()" aria-labelledby="add-user-title" (ngSubmit)="submit()"
        class="bg-kymo-panel border border-kymo-rule border-t-kymo-amber p-5 max-w-xl w-full space-y-4" (click)="$event.stopPropagation()">
    <h3 id="add-user-title" class="text-sm font-semibold uppercase tracking-[0.08em] text-kymo-ink">Add user</h3>

    <div class="grid sm:grid-cols-2 gap-3">
      <label class="flex flex-col gap-1">
        <span class="k-label">Username</span>
        <input class="k-input" name="username" required data-autofocus autocomplete="off" autocapitalize="none" spellcheck="false" [ngModel]="username()" (ngModelChange)="username.set($event)" />
        @if (username().trim() && !usernameValid()) { <span class="text-[11px] text-kymo-danger">2–64 chars: a-z, 0-9, . _ -</span> }
      </label>
      <label class="flex flex-col gap-1">
        <span class="k-label">Role</span>
        <select class="k-input" name="role" [ngModel]="role()" (ngModelChange)="role.set($event)">
          @for (r of assignable(); track r) { <option [value]="r">{{ r }}</option> }
        </select>
      </label>
      @if (method() === 'password') {
        <label class="flex flex-col gap-1">
          <span class="k-label">Name (optional)</span>
          <input class="k-input" name="name" autocomplete="off" [ngModel]="name()" (ngModelChange)="name.set($event)" />
        </label>
      }
      <label class="flex flex-col gap-1">
        <span class="k-label">Email (optional)</span>
        <input class="k-input" type="email" name="email" autocomplete="off" [ngModel]="email()" (ngModelChange)="email.set($event)" />
      </label>
    </div>

    <fieldset class="space-y-2">
      <legend class="k-label mb-1.5">How does the user get access?</legend>
      <div class="grid sm:grid-cols-2 gap-2">
        <label class="border p-3 cursor-pointer space-y-1 block" [class]="method() === 'link' ? 'border-kymo-amber bg-kymo-deep/40' : 'border-kymo-rule'">
          <span class="flex items-center gap-2 text-sm text-kymo-ink">
            <input type="radio" name="method" value="link" [ngModel]="method()" (ngModelChange)="method.set($event)" /> Setup link
          </span>
          <span class="block text-xs text-kymo-muted">The user chooses their password (recommended). Valid for 7 days.</span>
        </label>
        <label class="border p-3 cursor-pointer space-y-1 block" [class]="method() === 'password' ? 'border-kymo-amber bg-kymo-deep/40' : 'border-kymo-rule'">
          <span class="flex items-center gap-2 text-sm text-kymo-ink">
            <input type="radio" name="method" value="password" [ngModel]="method()" (ngModelChange)="method.set($event)" /> Temporary password
          </span>
          <span class="block text-xs text-kymo-muted">They must change it at first sign-in.</span>
        </label>
      </div>
      @if (method() === 'password') {
        <div class="space-y-1">
          <span class="k-label">Password</span>
          <div class="flex gap-2">
            <input class="k-input flex-1 min-w-0 font-mono" [type]="reveal() ? 'text' : 'password'" name="password" autocomplete="new-password" spellcheck="false"
                   placeholder="Leave empty to generate one" aria-label="Temporary password" [ngModel]="password()" (ngModelChange)="password.set($event)" />
            <button type="button" class="k-btn" (click)="reveal.set(!reveal())">{{ reveal() ? 'Hide' : 'Show' }}</button>
            <button type="button" class="k-btn" (click)="generate()">Generate</button>
          </div>
          @if (password() && !passwordValid()) { <span class="text-[11px] text-kymo-danger">At least {{ min }} characters.</span> }
          <p class="text-xs text-kymo-muted">Empty: KYMO generates a 20-character password and shows it once.</p>
        </div>
      }
    </fieldset>
    <p class="text-xs text-kymo-muted">KYMO does not send email; you pass the {{ method() === 'link' ? 'link' : 'password' }} on yourself.</p>

    <div class="flex justify-end gap-2">
      <button type="button" class="k-btn" (click)="closed.emit()">Cancel</button>
      <button type="submit" class="k-btn k-btn-primary" [disabled]="!usernameValid() || !passwordValid() || busy()">{{ busy() ? 'Creating…' : 'Add user' }}</button>
    </div>
  </form>
</div>
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class AddUserDialogComponent {
  private auth = inject(AuthService);
  private toast = inject(ToastService);

  /** Roles the caller may hand out. */
  assignable = input.required<Role[]>();
  created = output<{ result: AddUserResult; username: string }>();
  closed = output<void>();

  min = MIN_PASSWORD;
  username = signal('');
  name = signal('');
  email = signal('');
  role = signal<Role>('viewer');
  method = signal<AddUserMethod>('link');
  password = signal('');
  reveal = signal(false);
  busy = signal(false);

  usernameValid = computed(() => USERNAME_RE.test(this.username().trim().toLowerCase()));
  passwordValid = computed(() => this.method() === 'link' || !this.password() || [...this.password()].length >= MIN_PASSWORD);

  generate(): void { this.password.set(generatePassword()); this.reveal.set(true); }

  submit(): void {
    if (!this.usernameValid() || !this.passwordValid() || this.busy()) return;
    const username = this.username().trim().toLowerCase();
    const method = this.method();
    const name = method === 'password' ? this.name().trim() : '';
    const email = this.email().trim();
    this.busy.set(true);
    this.auth.createUser({
      username, role: this.role(), method,
      ...(name ? { name } : {}), ...(email ? { email } : {}),
      ...(method === 'password' && this.password() ? { password: this.password() } : {}),
    }).subscribe({
      next: result => { this.busy.set(false); this.created.emit({ result, username }); },
      error: err => { this.busy.set(false); this.toast.apiError(err, 'Could not add user'); },
    });
  }
}
