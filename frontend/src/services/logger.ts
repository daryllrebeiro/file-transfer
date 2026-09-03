type Level = 'debug' | 'info' | 'warn' | 'error';

const levels: Record<Level, number> = { debug: 10, info: 20, warn: 30, error: 40 };
const configured = (import.meta.env.VITE_LOG_LEVEL || (import.meta.env.DEV ? 'debug' : 'warn')) as Level;
const threshold = levels[configured] ?? levels.warn;

function write(level: Level, ...args: unknown[]) {
  if (levels[level] < threshold) return;
  const method = level === 'debug' ? 'log' : level;
  console[method](...args);
}

export const logger = {
  debug: (...args: unknown[]) => write('debug', ...args),
  info: (...args: unknown[]) => write('info', ...args),
  warn: (...args: unknown[]) => write('warn', ...args),
  error: (...args: unknown[]) => write('error', ...args),
};
