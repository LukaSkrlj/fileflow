import { DatePipe } from '@angular/common';
import { HttpErrorResponse, HttpEventType } from '@angular/common/http';
import { Component, inject, signal } from '@angular/core';
import { Fileflow, ObjectInfo, fileUrl } from './fileflow';
import { Transfer, formatBytes, formatRate, sparkline } from './transfer';

@Component({
  selector: 'app-root',
  imports: [DatePipe],
  templateUrl: './app.html',
})
export class App {
  private ff = inject(Fileflow);

  protected readonly ready = signal<'checking' | 'ready' | 'down'>('checking');
  protected readonly prefix = signal('');
  protected readonly uploadPrefix = signal('uploads/');
  protected readonly files = signal<ObjectInfo[]>([]);
  protected readonly listError = signal('');
  protected readonly transfers = signal<Transfer[]>([]);
  protected readonly playing = signal<ObjectInfo | null>(null);
  protected readonly dragging = signal(false);

  protected readonly fileUrl = fileUrl;
  protected readonly formatBytes = formatBytes;
  protected readonly formatRate = formatRate;
  protected readonly sparkline = sparkline;

  constructor() {
    this.ff.ready().subscribe({ next: () => this.ready.set('ready'), error: () => this.ready.set('down') });
    this.refresh();
  }

  refresh(): void {
    this.ff.list(this.prefix()).subscribe({
      next: (r) => {
        this.files.set(r.items);
        this.listError.set('');
      },
      error: (e: HttpErrorResponse) => this.listError.set(e.error?.error ?? `fileflow not reachable (${e.status})`),
    });
  }

  onDrop(ev: DragEvent): void {
    ev.preventDefault();
    this.dragging.set(false);
    this.uploadAll(ev.dataTransfer?.files ?? null);
  }

  uploadAll(list: FileList | null): void {
    for (const file of Array.from(list ?? [])) this.upload(file);
  }

  private upload(file: File): void {
    const t = this.track(new Transfer('upload', this.uploadPrefix() + file.name));
    t.total.set(file.size);
    const sub = this.ff.upload(t.key, file).subscribe({
      next: (ev) => {
        if (ev.type === HttpEventType.UploadProgress) t.progress(ev.loaded, ev.total);
        if (ev.type === HttpEventType.Response) {
          t.finish(`sha256 ${ev.body?.sha256 ?? '?'} · trace ${ev.headers.get('X-Trace-Id')}`);
          this.refresh();
        }
      },
      error: (e: HttpErrorResponse) => t.fail(`${e.status} ${e.error?.error ?? e.message} · trace ${e.error?.trace_id ?? '?'}`),
    });
    t.cancel = () => {
      sub.unsubscribe(); // aborts the XHR; the server aborts the multipart upload
      t.fail('cancelled', 'cancelled');
    };
  }

  /** Reads the body chunk by chunk to show real network speed; bytes are counted, not kept. */
  async stream(f: ObjectInfo): Promise<void> {
    const t = this.track(new Transfer('stream', f.key));
    const ac = new AbortController();
    t.cancel = () => ac.abort();
    try {
      const res = await this.ff.open(f.key, ac.signal);
      t.total.set(Number(res.headers.get('Content-Length')) || f.size);
      const reader = res.body!.getReader();
      let n = 0;
      for (;;) {
        const { done, value } = await reader.read();
        if (done) break;
        n += value.length;
        t.progress(n);
      }
      t.finish(`server ${res.headers.get('Server-Timing') ?? '-'} · trace ${res.headers.get('X-Trace-Id')}`);
    } catch (e) {
      if (ac.signal.aborted) t.fail('cancelled', 'cancelled');
      else t.fail(String(e));
    }
  }

  remove(f: ObjectInfo): void {
    if (!confirm(`Delete ${f.key}?`)) return;
    this.ff.delete(f.key).subscribe(() => {
      if (this.playing()?.key === f.key) this.playing.set(null);
      this.refresh();
    });
  }

  mediaKind(f: ObjectInfo): 'video' | 'audio' | 'image' | null {
    const kind = (f.content_type ?? '').split('/')[0];
    return kind === 'video' || kind === 'audio' || kind === 'image' ? kind : null;
  }

  clearFinished(): void {
    this.transfers.update((ts) => ts.filter((t) => t.status() === 'running'));
  }

  private track(t: Transfer): Transfer {
    this.transfers.update((ts) => [t, ...ts]);
    return t;
  }
}
