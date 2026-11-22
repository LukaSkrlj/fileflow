import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { TestBed } from '@angular/core/testing';
import { App } from './app';

describe('App', () => {
  it('lists files from the API', async () => {
    TestBed.configureTestingModule({
      imports: [App],
      providers: [provideHttpClient(), provideHttpClientTesting()],
    });
    const fixture = TestBed.createComponent(App);
    const http = TestBed.inject(HttpTestingController);

    http.expectOne('/readyz').flush('ready');
    http.expectOne((r) => r.url === '/v1/files').flush({
      items: [{ key: 'uploads/clip.mp4', size: 2048, content_type: 'video/mp4', mod_time: '2026-11-23T10:00:00Z' }],
    });
    await fixture.whenStable();

    const el = fixture.nativeElement as HTMLElement;
    expect(el.querySelector('td.key')?.textContent).toContain('uploads/clip.mp4');
    expect(el.querySelector('.badge')?.textContent).toContain('backend ready');
    expect(el.textContent).toContain('Play'); // video gets a player button
    http.verify();
  });
});
