import { Pipe, PipeTransform } from '@angular/core';
import { scoreTextClass, scoreBgClass } from '../utils/score';

/** `[class]="c.score | scoreClass"` → text color; `| scoreClass:'bg'` → background color. */
@Pipe({ name: 'scoreClass', standalone: true, pure: true })
export class ScoreClassPipe implements PipeTransform {
  transform(score: number | null | undefined, kind: 'text' | 'bg' = 'text'): string {
    return kind === 'bg' ? scoreBgClass(score) : scoreTextClass(score);
  }
}
