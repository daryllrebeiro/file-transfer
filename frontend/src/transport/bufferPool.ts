export class BufferPool {
  private subChunkPool: Uint8Array[] = [];
  private fullChunkPool: Uint8Array[] = [];
  private readonly maxSubChunkBuffers: number;
  private readonly maxFullChunkBuffers: number;
  private readonly subChunkSize: number;
  private readonly fullChunkSize: number;

  constructor(
    subChunkSize: number = 60 * 1024,
    fullChunkSize: number = 2 * 1024 * 1024,
    maxSubChunkBuffers: number = 32,
    maxFullChunkBuffers: number = 4
  ) {
    this.subChunkSize = subChunkSize;
    this.fullChunkSize = fullChunkSize;
    this.maxSubChunkBuffers = maxSubChunkBuffers;
    this.maxFullChunkBuffers = maxFullChunkBuffers;
  }

  acquireSubChunk(): Uint8Array {
    if (this.subChunkPool.length > 0) {
      return this.subChunkPool.pop()!;
    }
    return new Uint8Array(this.subChunkSize);
  }

  releaseSubChunk(buffer: Uint8Array): void {
    if (buffer.byteLength !== this.subChunkSize) return;
    if (this.subChunkPool.length < this.maxSubChunkBuffers) {
      this.subChunkPool.push(buffer);
    }
  }

  acquireFullChunk(): Uint8Array {
    if (this.fullChunkPool.length > 0) {
      return this.fullChunkPool.pop()!;
    }
    return new Uint8Array(this.fullChunkSize);
  }

  releaseFullChunk(buffer: Uint8Array): void {
    if (buffer.byteLength !== this.fullChunkSize) return;
    if (this.fullChunkPool.length < this.maxFullChunkBuffers) {
      this.fullChunkPool.push(buffer);
    }
  }

  clear(): void {
    this.subChunkPool = [];
    this.fullChunkPool = [];
  }

  getStats() {
    return {
      subChunkPoolSize: this.subChunkPool.length,
      fullChunkPoolSize: this.fullChunkPool.length,
    };
  }
}

export const bufferPool = new BufferPool();