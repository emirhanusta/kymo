import { Pipe, PipeTransform } from '@angular/core';
import { formatBytes } from '../utils/format';

@Pipe({ name: 'bytes', standalone: true, pure: true })
export class BytesPipe implements PipeTransform {
  transform(bytes: number | null | undefined): string {
    return formatBytes(bytes);
  }
}
