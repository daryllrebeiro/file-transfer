import { useCallback, useEffect, useState } from 'react';
import en from './en';
import de from './de';
import es from './es';
import fr from './fr';

export const locales = ['en', 'de', 'es', 'fr'] as const;
export type Locale = (typeof locales)[number];

const dictionaries: Record<Locale, Record<string, string>> = { en, de, es, fr };

function detect(): Locale {
  try {
    const saved = localStorage.getItem('relay:lang') as Locale | null;
    if (saved && locales.includes(saved)) return saved;
  } catch {
    // fall through to detection
  }
  const nav = (typeof navigator !== 'undefined' ? navigator.language.slice(0, 2) : 'en').toLowerCase() as Locale;
  return locales.includes(nav) ? nav : 'en';
}

let current: Locale = detect();
const subscribers = new Set<() => void>();

function emit() {
  subscribers.forEach(listener => listener());
}

export function t(key: string, vars?: Record<string, string | number>): string {
  const template = dictionaries[current][key] ?? dictionaries.en[key] ?? key;
  if (!vars) return template;
  return template.replace(/\{(\w+)\}/g, (match, name) => (name in vars ? String(vars[name]) : match));
}

export function setLocale(locale: Locale) {
  if (locale === current) return;
  current = locale;
  try {
    localStorage.setItem('relay:lang', locale);
  } catch {
    // best-effort persistence
  }
  if (typeof document !== 'undefined') {
    document.documentElement.lang = locale;
  }
  emit();
}

export function getLocale(): Locale {
  return current;
}

export function useT(): (key: string, vars?: Record<string, string | number>) => string {
  const [, setTick] = useState(0);
  useEffect(() => {
    const listener = () => setTick(value => value + 1);
    subscribers.add(listener);
    return () => { subscribers.delete(listener); };
  }, []);
  return useCallback((key: string, vars?: Record<string, string | number>) => t(key, vars), []);
}

export function useLocale(): { locale: Locale; change: (locale: Locale) => void } {
  const [, setTick] = useState(0);
  useEffect(() => {
    const listener = () => setTick(value => value + 1);
    subscribers.add(listener);
    return () => { subscribers.delete(listener); };
  }, []);
  return { locale: current, change: setLocale };
}
