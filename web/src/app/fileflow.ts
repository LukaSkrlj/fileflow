import { HttpClient, HttpEvent } from '@angular/common/http';
import { Injectable, inject } from '@angular/core';
import { Observable } from 'rxjs';

/** Mirrors storage.ObjectInfo in internal/storage/storage.go. */
export interface ObjectInfo {
  key: string;
  size: number;
  content_type?: string;
  etag?: string;
  sha256?: string;
  mod_time: string;
}

/** API path for a key; each segment is encoded so names with spaces, #, ? etc. survive. */
export function fileUrl(key: string): string {
  return '/v1/files/' + key.split('/').map(encodeURIComponent).join('/');
}

const HUMAN = { 'X-Client-Kind': 'human' };

@Injectable({ providedIn: 'root' })
export class Fileflow {
  private http = inject(HttpClient);

  list(prefix: string) {
    return this.http.get<{ items: ObjectInfo[] }>('/v1/files', { params: { prefix } });
  }

  /** XHR backend streams the File from disk and reports upload progress. */
  upload(key: string, file: File): Observable<HttpEvent<ObjectInfo>> {
    return this.http.put<ObjectInfo>(fileUrl(key), file, {
      headers: { ...HUMAN, 'Content-Type': file.type || 'application/octet-stream' },
      reportUploadProgress: true,
      observe: 'events',
    });
  }

  delete(key: string) {
    return this.http.delete(fileUrl(key), { headers: HUMAN });
  }

  ready() {
    return this.http.get('/readyz', { responseType: 'text' });
  }

  /** Raw fetch so the caller can read the body as a stream instead of buffering it. */
  async open(key: string, signal: AbortSignal): Promise<Response> {
    const res = await fetch(fileUrl(key), { headers: HUMAN, signal });
    if (!res.ok || !res.body) {
      const body = await res.json().catch(() => ({}));
      throw new Error(`${res.status} ${body.error ?? res.statusText}`);
    }
    return res;
  }
}
