import { computed, signal } from '@angular/core';

const SAMPLE_MS = 250;
const MAX_SAMPLES = 80;

/** One upload or streamed download, with live progress and a speed history for the chart. */
export class Transfer {
  readonly loaded = signal(0);
  readonly total = signal(0);
  readonly status = signal<'running' | 'done' | 'failed' | 'cancelled'>('running');
  readonly detail = signal('');
  /** Bytes per second, one sample every SAMPLE_MS. */
  readonly speeds = signal<number[]>([]);
  readonly ttfbMs = signal<number | null>(null);

  private readonly started = performance.now();
  private ended = signal<number | null>(null);
  private lastT = this.started;
  private lastBytes = 0;

  cancel: () => void = () => {};

  constructor(
    readonly kind: 'upload' | 'stream',
    readonly key: string,
  ) {}

  readonly percent = computed(() => (this.total() ? (100 * this.loaded()) / this.total() : 0));

  /** Average over the whole transfer; live while running. */
  readonly avgSpeed = computed(() => {
    const ms = (this.ended() ?? performance.now()) - this.started;
    return ms > 0 ? (this.loaded() * 1000) / ms : 0;
  });

  /** All bytes sent but no response yet: the server is committing (checksum, multipart complete). */
  readonly committing = computed(
    () => this.kind === 'upload' && this.status() === 'running' && this.total() > 0 && this.loaded() >= this.total(),
  );

  progress(loaded: number, total?: number): void {
    const now = performance.now();
    if (this.ttfbMs() === null && loaded > 0) this.ttfbMs.set(now - this.started);
    this.loaded.set(loaded);
    if (total) this.total.set(total);
    if (now - this.lastT >= SAMPLE_MS) {
      const bps = ((loaded - this.lastBytes) * 1000) / (now - this.lastT);
      this.speeds.update((s) => [...s, bps].slice(-MAX_SAMPLES));
      this.lastT = now;
      this.lastBytes = loaded;
    }
  }

  finish(detail: string): void {
    this.ended.set(performance.now());
    this.detail.set(detail);
    this.status.set('done');
  }

  fail(detail: string, status: 'failed' | 'cancelled' = 'failed'): void {
    this.ended.set(performance.now());
    this.detail.set(detail);
    this.status.set(status);
  }
}

/** SVG polyline points for a speed history scaled into a w×h box. */
export function sparkline(samples: number[], w: number, h: number): string {
  if (samples.length < 2) return '';
  const max = Math.max(...samples, 1);
  const step = w / (samples.length - 1);
  return samples.map((v, i) => `${(i * step).toFixed(1)},${(h - (v / max) * h).toFixed(1)}`).join(' ');
}

export function formatBytes(n: number): string {
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];
  let i = 0;
  while (n >= 1024 && i < units.length - 1) {
    n /= 1024;
    i++;
  }
  return `${i === 0 ? n : n.toFixed(1)} ${units[i]}`;
}

export function formatRate(bytesPerSec: number): string {
  return `${(bytesPerSec / 1e6).toFixed(1)} MB/s`;
}
