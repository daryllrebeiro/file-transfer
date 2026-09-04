export type HistoryEntry = { id: string; name: string; size: number; ts: number; outcome: 'created' | 'sent' | 'failed' };

const KEY = 'relay:history';
const MAX = 20;

function read(): HistoryEntry[] {
  try {
    const parsed = JSON.parse(localStorage.getItem(KEY) || '[]');
    return Array.isArray(parsed) ? parsed : [];
  } catch {
    return [];
  }
}

function write(entries: HistoryEntry[]) {
  try {
    localStorage.setItem(KEY, JSON.stringify(entries));
  } catch {
    // storage full or unavailable: history is best-effort
  }
}

export function addHistory(entry: HistoryEntry) {
  const entries = read().filter(existing => existing.id !== entry.id);
  entries.unshift(entry);
  write(entries.slice(0, MAX));
}

export function updateHistory(id: string, patch: Partial<HistoryEntry>) {
  const entries = read();
  const index = entries.findIndex(entry => entry.id === id);
  if (index >= 0) {
    entries[index] = { ...entries[index], ...patch };
    write(entries);
  }
}

export function clearHistory() {
  write([]);
}

export function getHistory(): HistoryEntry[] {
  return read();
}
