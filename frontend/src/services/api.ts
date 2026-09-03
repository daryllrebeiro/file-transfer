import type { Metadata } from '../types';
const apiURL = import.meta.env.VITE_API_URL ?? 'http://localhost:8080';
export type TransferLimits = { maxFileSize: number; maxChunkSize: number };
export async function createTransfer(metadata: Metadata): Promise<{ id: string; url: string; senderToken: string; expiresAt: string }> { const response = await fetch(`${apiURL}/api/transfers`, { method: 'POST', headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'XMLHttpRequest' }, body: JSON.stringify(metadata) }); if (!response.ok) { const detail = await response.text(); throw new Error(detail || (response.status === 400 ? 'Invalid transfer metadata.' : 'The transfer service is unavailable.')); } return response.json(); }
export async function getTransferLimits(signal?: AbortSignal): Promise<TransferLimits> { const response = await fetch(`${apiURL}/api/limits`, { signal }); if (!response.ok) throw new Error('Could not load transfer limits.'); return response.json(); }
export function socketURL(id: string): string { const configured = import.meta.env.VITE_WS_URL; return configured ? `${configured.replace(/\/$/, '')}/ws/${id}` : `${apiURL.replace(/^http/, 'ws')}/ws/${id}`; }
