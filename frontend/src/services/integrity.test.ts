import { describe, expect, it } from 'vitest';
import { createHash, digestHex, hashFile, hashFileSync } from './integrity';

describe('integrity', () => {
  it('matches the known SHA-256 digest', () => {
    const hash = createHash();
    hash.update(new TextEncoder().encode('hello'));
    expect(digestHex(hash)).toBe('2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824');
  });
  
  it('hashes a File incrementally (sync fallback)', async () => {
    const file = new File(['hello world'], 'hello.txt');
    expect(await hashFileSync(file, 2)).toBe('b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9');
  });
  
  it('hashes a File incrementally (async with worker fallback)', async () => {
    const file = new File(['hello world'], 'hello.txt');
    // Should use sync fallback since Worker is not available in test env
    expect(await hashFile(file, 2)).toBe('b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9');
  });
});
