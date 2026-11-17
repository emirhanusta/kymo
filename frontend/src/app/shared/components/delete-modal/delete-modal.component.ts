import {
  Component, ChangeDetectionStrategy, Input, Output, EventEmitter,
} from '@angular/core';
import { DialogDirective } from '../../directives/dialog.directive';

@Component({
  selector: 'app-delete-modal',
  standalone: true,
  imports: [DialogDirective],
  templateUrl: './delete-modal.component.html',
  styleUrl: './delete-modal.component.scss',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class DeleteModalComponent {
  @Input() containerName = '';
  @Output() action = new EventEmitter<'soft' | 'ignore' | 'cancel'>();

  /** Archive is the safe default so the primary button is immediately usable. */
  selected: 'soft' | 'ignore' | null = 'soft';

  confirm(): void {
    if (this.selected) this.action.emit(this.selected);
  }

  cancel(): void {
    this.action.emit('cancel');
  }
}
