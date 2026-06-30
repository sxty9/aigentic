import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
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

// Chats persist per-device in localStorage (no backend, no cross-device sync yet — a deliberate
// v1 choice; the hook is the single seam to swap in a server store later).
const STORE_KEY = 'aigentic.chats.v1';

function load(): Chat[] {
  try {
    const raw = localStorage.getItem(STORE_KEY);
    const v = raw ? (JSON.parse(raw) as Chat[]) : [];
    return Array.isArray(v) ? v : [];
  } catch {
    return [];
  }
}

function persist(chats: Chat[]) {
  try {
    localStorage.setItem(STORE_KEY, JSON.stringify(chats));
  } catch {
    // quota / private mode — chats just won't survive a reload
  }
}

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

// clean strips the leading "Assistant:" the transcript framing can echo, plus any file-context
// tags carried over from a seeded handoff. Shared by the store (seed) and the chat view (replies).
export function clean(s: string): string {
  return s
    .replace(/^\s*Assistant:\s*/i, '')
    .replace(/<\/?(file|attachment)\b[^>]*>/g, '')
    .trim();
}

export interface ChatStore {
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

// useChats owns the chat list + the active selection, persisting to localStorage. On first mount
// it adopts a "continue in chat" handoff seed (from the Files dialog) as a fresh chat, else it
// ensures one empty chat exists so the composer always has somewhere to write.
export function useChats(): ChatStore {
  const [chats, setChats] = useState<Chat[]>(() => load());
  const [activeId, setActiveId] = useState<string>(() => load()[0]?.id ?? '');
  const [search, setSearch] = useState('');
  const initialized = useRef(false);

  useEffect(() => {
    persist(chats);
  }, [chats]);

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

  // One-time bootstrap: seed handoff, or guarantee a live chat to type into.
  useEffect(() => {
    if (initialized.current) return;
    initialized.current = true;
    let seedMsgs: Msg[] | null = null;
    try {
      const raw = localStorage.getItem(CHAT_SEED_KEY);
      if (raw) {
        localStorage.removeItem(CHAT_SEED_KEY);
        const s = JSON.parse(raw) as ChatSeed;
        if (s?.prompt || s?.answer) {
          seedMsgs = [
            { role: 'user', content: s.prompt || '(files)' },
            { role: 'assistant', content: clean(s.answer || ''), engine: s.engine, model: s.model },
          ];
        }
      }
    } catch {
      // ignore a malformed seed
    }
    if (seedMsgs) {
      const c: Chat = { id: newId(), title: titleOf(seedMsgs), messages: seedMsgs, updatedAt: Date.now() };
      setChats((prev) => [c, ...prev]);
      setActiveId(c.id);
    } else {
      setChats((prev) => {
        if (prev.length > 0) {
          setActiveId((cur) => cur || prev[0].id);
          return prev;
        }
        const c: Chat = { id: newId(), title: 'New chat', messages: [], updatedAt: Date.now() };
        setActiveId(c.id);
        return [c];
      });
    }
  }, []);

  const active = chats.find((c) => c.id === activeId) ?? null;

  const filtered = useMemo(() => {
    const sorted = [...chats].sort((a, b) => b.updatedAt - a.updatedAt);
    const qx = search.trim().toLowerCase();
    if (!qx) return sorted;
    return sorted.filter(
      (c) => c.title.toLowerCase().includes(qx) || c.messages.some((m) => m.content.toLowerCase().includes(qx)),
    );
  }, [chats, search]);

  return { chats, active, activeId, search, setSearch, filtered, newChat, selectChat, deleteChat, setActiveMessages };
}
