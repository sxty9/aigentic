import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import type { ServiceApiClient } from '@holistic/ui';
import { cleanAnswer } from './aiFiles';
import { CHAT_SEED_KEY, type ChatSeed } from './types';

export interface Msg {
  role: 'user' | 'assistant';
  content: string;
  engine?: string;
  model?: string;
}

export interface Chat {
  id: string;
  title: string;
  messages: Msg[];
  updatedAt: number;
}

// Chats are stored SERVER-SIDE per holistic account (GET/PUT /chats), so they follow the user
// across devices. This hook loads them on mount and writes the whole list back (debounced) on
// change. The cross-tab handoff seed still rides localStorage (a transient same-device baton).

function newId(): string {
  try {
    return crypto.randomUUID();
  } catch {
    return `c-${Date.now()}-${Math.floor(Math.random() * 1e9)}`;
  }
}

// titleOf derives a chat's sidebar label from its first user message.
function titleOf(messages: Msg[]): string {
  const first = messages.find((m) => m.role === 'user');
  const t = (first?.content ?? '').trim().replace(/\s+/g, ' ');
  if (!t) return 'New chat';
  return t.length > 48 ? `${t.slice(0, 48)}…` : t;
}

function seedMessages(): Msg[] | null {
  try {
    const raw = localStorage.getItem(CHAT_SEED_KEY);
    if (!raw) return null;
    localStorage.removeItem(CHAT_SEED_KEY);
    const s = JSON.parse(raw) as ChatSeed;
    if (!s?.prompt && !s?.answer) return null;
    return [
      { role: 'user', content: s.prompt || '(files)' },
      { role: 'assistant', content: cleanAnswer(s.answer || ''), engine: s.engine, model: s.model },
    ];
  } catch {
    return null;
  }
}

export interface ChatStore {
  loading: boolean;
  chats: Chat[];
  active: Chat | null;
  activeId: string;
  search: string;
  setSearch: (s: string) => void;
  filtered: Chat[];
  newChat: () => void;
  selectChat: (id: string) => void;
  deleteChat: (id: string) => void;
  setActiveMessages: (msgs: Msg[]) => void;
}

export function useChats(api: ServiceApiClient): ChatStore {
  const [chats, setChats] = useState<Chat[]>([]);
  const [activeId, setActiveId] = useState<string>('');
  const [search, setSearch] = useState('');
  const [loading, setLoading] = useState(true);

  const initialized = useRef(false);
  const apiRef = useRef(api);
  apiRef.current = api;
  const latest = useRef<Chat[]>([]);
  latest.current = chats;
  const dirty = useRef(false);

  // Load from the server once, then adopt a handoff seed (fresh chat) or ensure one empty chat
  // exists so the composer always has somewhere to write.
  useEffect(() => {
    if (initialized.current) return;
    initialized.current = true;
    let cancelled = false;
    (async () => {
      let loaded: Chat[] = [];
      try {
        const got = await api.get<Chat[]>('chats');
        if (Array.isArray(got)) loaded = got;
      } catch {
        // no stored chats / offline — start fresh
      }
      if (cancelled) return;
      const seed = seedMessages();
      let next = loaded;
      let active = loaded[0]?.id ?? '';
      if (seed) {
        const c: Chat = { id: newId(), title: titleOf(seed), messages: seed, updatedAt: Date.now() };
        next = [c, ...loaded];
        active = c.id;
      } else if (loaded.length === 0) {
        const c: Chat = { id: newId(), title: 'New chat', messages: [], updatedAt: Date.now() };
        next = [c];
        active = c.id;
      }
      setChats(next);
      setActiveId(active);
      setLoading(false);
    })();
    return () => {
      cancelled = true;
    };
  }, [api]);

  // Persist the whole list (debounced) on change; flush a pending write on unmount.
  useEffect(() => {
    if (loading) return;
    dirty.current = true;
    const t = window.setTimeout(() => {
      const snapshot = latest.current;
      apiRef.current
        .put('chats', snapshot)
        .then(() => {
          dirty.current = false;
        })
        .catch(() => {
          /* keep dirty; a later change (or unmount flush) retries */
        });
    }, 500);
    return () => window.clearTimeout(t);
  }, [chats, loading]);

  useEffect(
    () => () => {
      if (dirty.current) void apiRef.current.put('chats', latest.current).catch(() => {});
    },
    [],
  );

  const selectChat = useCallback((id: string) => setActiveId(id), []);

  const newChat = useCallback(() => {
    const c: Chat = { id: newId(), title: 'New chat', messages: [], updatedAt: Date.now() };
    setChats((prev) => [c, ...prev]);
    setActiveId(c.id);
  }, []);

  const deleteChat = useCallback((id: string) => {
    setChats((prev) => {
      const next = prev.filter((c) => c.id !== id);
      setActiveId((cur) => (cur === id ? next[0]?.id ?? '' : cur));
      return next;
    });
  }, []);

  const setActiveMessages = useCallback(
    (msgs: Msg[]) => {
      setChats((prev) =>
        prev.map((c) => (c.id === activeId ? { ...c, messages: msgs, title: titleOf(msgs), updatedAt: Date.now() } : c)),
      );
    },
    [activeId],
  );

  const active = chats.find((c) => c.id === activeId) ?? null;

  const filtered = useMemo(() => {
    const sorted = [...chats].sort((a, b) => b.updatedAt - a.updatedAt);
    const qx = search.trim().toLowerCase();
    if (!qx) return sorted;
    return sorted.filter(
      (c) => c.title.toLowerCase().includes(qx) || c.messages.some((m) => m.content.toLowerCase().includes(qx)),
    );
  }, [chats, search]);

  return { loading, chats, active, activeId, search, setSearch, filtered, newChat, selectChat, deleteChat, setActiveMessages };
}
