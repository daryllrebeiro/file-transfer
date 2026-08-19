const blobFallbackLimit = 250 * 1024 * 1024;
type Writable = { write(data: Uint8Array): Promise<void>; close(): Promise<void> };
type SavePicker = (options: { suggestedName: string; types: Array<{ description: string; accept: Record<string, string[]> }> }) => Promise<{ createWritable(): Promise<Writable> }>;

export type ReceiverSink = { write: (data: Uint8Array) => Promise<void>; close: () => Promise<void>; blobParts: BlobPart[] | undefined };

export async function createReceiverSink(fileName: string, mimeType: string, fileSize: number): Promise<ReceiverSink> {
  const picker = (window as unknown as { showSaveFilePicker?: SavePicker }).showSaveFilePicker;
  if (picker) {
    const handle = await picker({ suggestedName: fileName, types: [{ description: mimeType || 'File', accept: { [mimeType || 'application/octet-stream']: ['.' + (fileName.split('.').pop() || 'bin')] } }] });
    const writable = await handle.createWritable();
    return { write: data => writable.write(data), close: () => writable.close(), blobParts: undefined };
  }
  if (fileSize > blobFallbackLimit) throw new Error('This browser cannot safely save files larger than 250 MB. Use a Chromium-based browser or another device.');
  const parts: BlobPart[] = [];
  return { write: async data => { parts.push(data.buffer.slice(data.byteOffset, data.byteOffset + data.byteLength) as ArrayBuffer); }, close: async () => undefined, blobParts: parts };
}