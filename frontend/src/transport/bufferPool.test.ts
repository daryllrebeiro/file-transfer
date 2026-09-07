import { describe, it, expect, beforeEach } from 'vitest';
import { BufferPool } from './bufferPool';

describe('BufferPool', () => {
  let pool: BufferPool;

  beforeEach(() => {
    pool = new BufferPool(1024, 4096, 4, 2);
  });

  it('should acquire and release sub-chunk buffers', () => {
    const buf1 = pool.acquireSubChunk();
    expect(buf1.byteLength).toBe(1024);

    const buf2 = pool.acquireSubChunk();
    expect(buf2.byteLength).toBe(1024);

    pool.releaseSubChunk(buf1);
    pool.releaseSubChunk(buf2);

    const stats = pool.getStats();
    expect(stats.subChunkPoolSize).toBe(2);
  });

  it('should acquire and release full-chunk buffers', () => {
    const buf1 = pool.acquireFullChunk();
    expect(buf1.byteLength).toBe(4096);

    pool.releaseFullChunk(buf1);

    const stats = pool.getStats();
    expect(stats.fullChunkPoolSize).toBe(1);
  });

  it('should not exceed max pool size for sub-chunks', () => {
    const buffers: Uint8Array[] = [];
    for (let i = 0; i < 10; i++) {
      buffers.push(pool.acquireSubChunk());
    }
    for (const buf of buffers) {
      pool.releaseSubChunk(buf);
    }
    const stats = pool.getStats();
    expect(stats.subChunkPoolSize).toBe(4);
  });

  it('should not exceed max pool size for full-chunks', () => {
    const buffers: Uint8Array[] = [];
    for (let i = 0; i < 10; i++) {
      buffers.push(pool.acquireFullChunk());
    }
    for (const buf of buffers) {
      pool.releaseFullChunk(buf);
    }
    const stats = pool.getStats();
    expect(stats.fullChunkPoolSize).toBe(2);
  });

  it('should return pooled buffers when available', () => {
    const buf1 = pool.acquireSubChunk();
    pool.releaseSubChunk(buf1);

    const buf2 = pool.acquireSubChunk();
    // Pool should reuse the buffer
    expect(buf2).toBe(buf1);
  });

  it('should clear all buffers on clear()', () => {
    const buf1 = pool.acquireSubChunk();
    const buf2 = pool.acquireFullChunk();
    pool.releaseSubChunk(buf1);
    pool.releaseFullChunk(buf2);

    pool.clear();

    const stats = pool.getStats();
    expect(stats.subChunkPoolSize).toBe(0);
    expect(stats.fullChunkPoolSize).toBe(0);
  });

  it('should ignore buffers of wrong size on release', () => {
    const buf1 = pool.acquireSubChunk();
    const buf2 = pool.acquireFullChunk();
    pool.releaseSubChunk(buf1);
    pool.releaseFullChunk(buf2);

    const wrongSize = new Uint8Array(2048);
    pool.releaseSubChunk(wrongSize);
    pool.releaseFullChunk(wrongSize);

    const stats = pool.getStats();
    expect(stats.subChunkPoolSize).toBe(1);
    expect(stats.fullChunkPoolSize).toBe(1);
  });
});