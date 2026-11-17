import { Injectable, signal } from '@angular/core';

/** Lets detail pages contribute a breadcrumb label (e.g. the container name) to the navbar. */
@Injectable({ providedIn: 'root' })
export class PageContextService {
  readonly detailLabel = signal<string | null>(null);
}
