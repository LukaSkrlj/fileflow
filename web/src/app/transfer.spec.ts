import { fileUrl } from './fileflow';
import { formatBytes, sparkline } from './transfer';

describe('fileUrl', () => {
  it('encodes each segment but keeps slashes', () => {
    expect(fileUrl('uploads/my file #1?.txt')).toBe('/v1/files/uploads/my%20file%20%231%3F.txt');
  });
});

describe('sparkline', () => {
  it('scales samples into the box, peak at the top', () => {
    expect(sparkline([0, 50, 100], 100, 10)).toBe('0.0,10.0 50.0,5.0 100.0,0.0');
  });
  it('draws nothing until there are two samples', () => {
    expect(sparkline([5], 100, 10)).toBe('');
  });
});

describe('formatBytes', () => {
  it('uses binary units', () => {
    expect(formatBytes(512)).toBe('512 B');
    expect(formatBytes(16 * 1024 * 1024)).toBe('16.0 MiB');
  });
});
