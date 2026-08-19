import { describe, expect, it } from 'vitest';
import { frameChunk, parseChunk } from './transferClient';

describe('chunk framing', () => {
  it('round trips index and bytes', () => {
    const frame = frameChunk(42, new TextEncoder().encode('hello').buffer);
    const parsed = parseChunk(frame);
    expect(parsed.index).toBe(42);
    expect(new TextDecoder().decode(parsed.bytes)).toBe('hello');
  });
  it('rejects truncated and malformed frames', () => {
    expect(() => parseChunk(new ArrayBuffer(15))).toThrow('Invalid chunk');
    const frame = frameChunk(1, new ArrayBuffer(2));
    new DataView(frame).setBigUint64(8, 99n);
    expect(() => parseChunk(frame)).toThrow('Invalid chunk');
  });
});
