// The aigentic ChatAdapter — how aigentic drives the ONE shared <Chat> building block from
// @holisdk/ui. The shared chat owns the whole experience (many conversations, machine+model
// choice, model-tagged answers, reload-surviving history) but stores nothing itself: this adapter
// is the single source of truth for aigentic's conversations, so there is never a second data path
// to them and no second, poorer chat.
//
// Persistence is the SAME per-account server store the previous aigentic chat used (GET/PUT
// /chats, an opaque JSON array keyed by the server-stamped Subject), so history keeps following
// the user across devices and survives a restart. The machine list comes from GET /models, and
// every turn runs through the SAME single backend access point the "Ask AI" panels use
// (runAigentic → POST /run), inheriting per-user creds, Auto routing and multimodal for free.
import type {
  ChatAdapter,
  ChatEngine,
  ChatMessage,
  Conversation,
  NewConversation,
  SendInput,
  ServiceApiClient,
} from '@holisdk/ui';
import { runAigentic } from './aiExchange';
import { cleanAnswer, type InlinePart } from './aiFiles';
import { CLAUDE_FALLBACK, ENGINES, usesEffort } from './EnginePicker';
import type { AigenticRequest, ChatSeed, ModelCatalog } from './types';

// The per-turn extras the shared SendInput has no field for — attached files and the Claude effort
// level. The chat wrapper owns these (composer accessory + drag-drop) and the adapter reads the
// live value at send time, then calls onSent to clear the attachments.
export interface ChatExtras {
  inline: InlinePart[];
  effort: string;
}

// A stored conversation. Mirrors the shared Conversation plus the messages themselves and the
// last machine/model used, so reopening a conversation restores its selection. The backend treats
// the whole list as opaque JSON, so these extra fields are safe to add.
interface StoredMsg {
  role: 'user' | 'assistant';
  content: string;
  // For an assistant turn: the model label that produced it (Kennzeichnungspflicht).
  model?: string;
}
interface StoredChat {
  id: string;
  title: string;
  updatedAt: number;
  engineId?: string;
  modelId?: string;
  messages: StoredMsg[];
}

function newId(): string {
  try {
    return crypto.randomUUID();
  } catch {
    return `c-${Date.now()}-${Math.floor(Math.random() * 1e9)}`;
  }
}

// titleOf derives a conversation's rail label from its first user message.
function titleOf(text: string): string {
  const t = text.trim().replace(/\s+/g, ' ');
  if (!t) return 'New chat';
  return t.length > 48 ? `${t.slice(0, 48)}…` : t;
}

// The adapter exposes the ChatAdapter contract plus consumeSeed (the Files "Ask AI" → chat
// handoff), which the wrapper calls once on mount.
export interface AigenticChatAdapter extends ChatAdapter {
  consumeSeed(seed: ChatSeed): Promise<string>;
}

// makeAigenticAdapter builds the adapter over an aigentic-scoped ServiceApiClient. `extras`
// supplies the live attachments + effort at send time; `onSent` clears the attachments after a
// turn is accepted.
export function makeAigenticAdapter(
  api: ServiceApiClient,
  extras: () => ChatExtras,
  onSent: () => void,
): AigenticChatAdapter {
  // The whole conversation list, loaded once and then the single in-memory truth. Every mutation
  // persists the full list (an await'd PUT) before returning, so a reload always reflects it and no
  // write is lost.
  let chats: StoredChat[] = [];
  let loaded: Promise<void> | null = null;
  let catalog: ModelCatalog | null = null;

  function ensureLoaded(): Promise<void> {
    if (!loaded) {
      loaded = api
        .get<StoredChat[]>('chats')
        .then((got) => {
          chats = Array.isArray(got) ? got : [];
        })
        .catch(() => {
          chats = []; // no stored history / offline — start empty
        });
    }
    return loaded;
  }

  async function save(): Promise<void> {
    try {
      await api.put('chats', chats);
    } catch {
      // Best-effort: a failed write keeps the in-memory list; a later turn retries the whole list.
    }
  }

  function byId(id: string): StoredChat | undefined {
    return chats.find((c) => c.id === id);
  }

  function toConversation(c: StoredChat): Conversation {
    return { id: c.id, title: c.title, updatedAt: c.updatedAt, engineId: c.engineId, modelId: c.modelId };
  }

  function toMessages(c: StoredChat): ChatMessage[] {
    return c.messages.map((m, i) => ({ id: `${c.id}:${i}`, role: m.role, content: m.content, model: m.model }));
  }

  return {
    // The machines + models the user may pick from — the SAME set as the "Ask AI" panels. Auto
    // (choose) leads and carries a single pseudo-model; a Claude machine lists the static models
    // (or the fallback); the local machine lists whatever ollama has pulled, and is omitted when it
    // has none (there is nothing local to run).
    async engines(): Promise<ChatEngine[]> {
      if (!catalog) {
        catalog = await api.get<ModelCatalog>('models').catch(() => ({ claude: [], ollama: [] }));
      }
      const claude = (catalog.claude.length ? catalog.claude : CLAUDE_FALLBACK).map((m) => ({ id: m.id, label: m.label }));
      const ollama = catalog.ollama.map((m) => ({ id: m, label: m }));
      const out: ChatEngine[] = [];
      for (const e of ENGINES) {
        if (e.value === 'choose') out.push({ id: e.value, label: e.label, models: [{ id: '', label: 'Auto' }] });
        else if (e.value === 'ollama') {
          if (ollama.length) out.push({ id: e.value, label: e.label, models: ollama });
        } else out.push({ id: e.value, label: e.label, models: claude });
      }
      return out;
    },

    async listConversations(): Promise<Conversation[]> {
      await ensureLoaded();
      return [...chats].sort((a, b) => b.updatedAt - a.updatedAt).map(toConversation);
    },

    async loadMessages(conversationId: string): Promise<ChatMessage[]> {
      await ensureLoaded();
      const c = byId(conversationId);
      return c ? toMessages(c) : [];
    },

    async createConversation(init?: NewConversation): Promise<Conversation> {
      await ensureLoaded();
      const c: StoredChat = {
        id: newId(),
        title: init?.title || 'New chat',
        updatedAt: Date.now(),
        engineId: init?.engineId,
        modelId: init?.modelId,
        messages: [],
      };
      chats.unshift(c);
      await save();
      return toConversation(c);
    },

    async deleteConversation(conversationId: string): Promise<void> {
      await ensureLoaded();
      chats = chats.filter((c) => c.id !== conversationId);
      await save();
    },

    // send runs one user turn. Multi-turn is stateless on the backend: the whole conversation is
    // re-sent as one transcript prompt, so the model continues after the trailing "Assistant:".
    // Attached files ride along on the shared `inline` path; effort applies only to Claude engines.
    async send(input: SendInput): Promise<ChatMessage> {
      await ensureLoaded();
      const c = byId(input.conversationId);
      const prior: StoredMsg[] = c ? c.messages : [];
      const turns = [...prior, { role: 'user' as const, content: input.text }];
      const transcript = turns.map((m) => `${m.role === 'user' ? 'User' : 'Assistant'}: ${m.content}`).join('\n\n') + '\n\nAssistant:';

      const { inline, effort } = extras();
      const data: AigenticRequest = { prompt: transcript };
      if (input.modelId) data.model = input.modelId;
      if (effort && usesEffort(input.engineId)) data.claude = { effort };
      if (inline.length) data.inline = inline;

      const out = await runAigentic(api, input.engineId, data);
      const answer = cleanAnswer(out.output);
      const model = out.model || out.engine || undefined;

      if (c) {
        c.messages = [...prior, { role: 'user', content: input.text }, { role: 'assistant', content: answer, model }];
        c.updatedAt = Date.now();
        c.engineId = input.engineId;
        c.modelId = input.modelId;
        // Keep the rail newest-first: the shared chat sorts by updatedAt via listConversations, but
        // persisting the move keeps it stable across a reload.
        chats = [c, ...chats.filter((x) => x.id !== c.id)];
        await save();
      }
      onSent();
      const idx = c ? c.messages.length - 1 : 0;
      return { id: `${input.conversationId}:${idx}`, role: 'assistant', content: answer, model, createdAt: Date.now() };
    },

    // consumeSeed adopts the Files "Ask AI" → chat handoff: it opens a fresh conversation
    // pre-filled with the one exchange the user already ran, and returns its id so the wrapper can
    // select it. No second data path — the seeded conversation is a normal stored conversation.
    async consumeSeed(seed: ChatSeed): Promise<string> {
      await ensureLoaded();
      const prompt = seed.prompt || '(files)';
      const c: StoredChat = {
        id: newId(),
        title: titleOf(prompt),
        updatedAt: Date.now(),
        engineId: seed.engine,
        modelId: seed.model,
        messages: [
          { role: 'user', content: prompt },
          { role: 'assistant', content: cleanAnswer(seed.answer || ''), model: seed.model || seed.engine || undefined },
        ],
      };
      chats.unshift(c);
      await save();
      return c.id;
    },
  };
}
