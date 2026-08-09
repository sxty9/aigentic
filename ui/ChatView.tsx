import { useEffect, useLayoutEffect, useRef, useState, type DragEvent, type KeyboardEvent } from 'react';
import {
  AskChoice,
  Badge,
  Box,
  Button,
  EmptyState,
  FilesIcon,
  ScrollArea,
  Spinner,
  Stack,
  Text,
  Textarea,
  UploadControl,
  formatBytes,
  type ServiceApiClient,
  type ServiceContextProps,
} from '@holisdk/ui';
import { EnginePicker, pickerFields, type Picker } from './EnginePicker';
import { AnswerBody, EngineTag, runAigentic } from './aiExchange';
import { cleanAnswer, fileToInline, type InlinePart } from './aiFiles';
import { type Msg } from './chatStore';
import { FilesPicker } from './FilesPicker';
import { useOllama } from './ollamaStatus';
import type { AigenticRequest } from './types';

// Per-chat scroll memory (session-scoped, keyed by chat id): returning to a chat restores where
// you last were, while a new message jumps to the bottom.
const scrollMem = new Map<string, number>();
const SCROLL_ID = 'aigentic-chat-scroll';
const scrollEl = () => document.getElementById(SCROLL_ID);

// msgText is a message's text for the re-sent transcript. An assistant turn that was ONLY a
// structured question (no prose) still needs words so the model keeps context on the next turn;
// fall back to the question text.
function msgText(m: Msg): string {
  if (m.content) return m.content;
  if (m.ask) return m.ask.questions.map((q) => q.question).join('\n');
  return '';
}

// Attachment bounds: keep well under the backend's 32 MB /run cap and a sane file count so a
// stray folder-drop doesn't stall the browser.
const MAX_ATTACH = 25;
const MAX_ATTACH_BYTES = 24 * 1024 * 1024;

const baseName = (p: string) => p.split('/').pop() || p;
const mmss = (s: number) => `${Math.floor(s / 60)}:${String(Math.max(0, s) % 60).padStart(2, '0')}`;

// mergeAttach appends new parts under the count + byte caps, reporting how many were dropped so the
// caller can tell the user rather than silently truncating.
function mergeAttach(existing: InlinePart[], incoming: InlinePart[]): { next: InlinePart[]; dropped: number } {
  const next = [...existing];
  let bytes = existing.reduce((n, p) => n + p.content.length, 0);
  let dropped = 0;
  for (const p of incoming) {
    if (next.length >= MAX_ATTACH || bytes + p.content.length > MAX_ATTACH_BYTES) {
      dropped += 1;
      continue;
    }
    next.push(p);
    bytes += p.content.length;
  }
  return { next, dropped };
}

// ChatView is the conversation pane for one chat. It is controlled: the message list lives in the
// chat store (so it persists + drives the sidebar), and the picker is owned by the parent (so the
// engine choice survives switching chats). Multi-turn is stateless on the backend — the whole
// conversation is sent as one transcript prompt to /run, inheriting per-user creds, Auto routing
// and multimodal for free. Files can ride along as context: dragged/attached from the desktop, or
// picked from the user's Holistic Files share (both via the shared `inline` path).
export function ChatView({
  chatId,
  api,
  apiFor,
  ui,
  picker,
  messages,
  onMessages,
}: {
  chatId: string;
  api: ServiceApiClient;
  apiFor: ServiceContextProps['apiFor'];
  ui: ServiceContextProps['ui'];
  picker: Picker;
  messages: Msg[];
  onMessages: (m: Msg[]) => void;
}) {
  const [input, setInput] = useState('');
  const [busy, setBusy] = useState(false);
  const [attached, setAttached] = useState<InlinePart[]>([]);
  const [pickerOpen, setPickerOpen] = useState(false);
  const [dragOver, setDragOver] = useState(false);
  const prevLen = useRef(messages.length);

  // Live local-model residency (from ollama's /api/ps): staged progress + a keep-alive readout.
  const live = useOllama(api, { engine: picker.engine, model: picker.model, busy });

  // On open/switch: restore the chat's last scroll position, else start at the bottom.
  useLayoutEffect(() => {
    const el = scrollEl();
    if (!el) return;
    const saved = scrollMem.get(chatId);
    el.scrollTop = saved != null ? saved : el.scrollHeight;
    prevLen.current = messages.length;
  }, [chatId]);

  // A new message (sent or received) jumps to the bottom.
  useEffect(() => {
    const el = scrollEl();
    if (el && messages.length > prevLen.current) el.scrollTop = el.scrollHeight;
    prevLen.current = messages.length;
  }, [messages]);

  // Keep the progress row in view while a reply is pending.
  useEffect(() => {
    if (busy) {
      const el = scrollEl();
      if (el) el.scrollTop = el.scrollHeight;
    }
  }, [busy]);

  function saveScroll() {
    const el = scrollEl();
    if (el) scrollMem.set(chatId, el.scrollTop);
  }

  async function addFiles(files: File[]) {
    if (!files.length) return;
    const parts = (await Promise.all(files.map(fileToInline))).filter(Boolean) as InlinePart[];
    addParts(parts);
  }

  function addParts(parts: InlinePart[]) {
    if (!parts.length) return;
    const { next, dropped } = mergeAttach(attached, parts);
    setAttached(next);
    if (dropped > 0) ui.toast({ title: `${dropped} Datei(en) nicht angehängt`, description: 'Zu viele oder zu groß (max. 25 Dateien, 24 MB).', variant: 'error' });
  }

  function onDrop(e: DragEvent<HTMLDivElement>) {
    e.preventDefault();
    setDragOver(false);
    const files = Array.from(e.dataTransfer?.files ?? []);
    if (files.length) void addFiles(files);
  }

  // send(answer?) posts a turn. With no argument it sends the composer's text; with an argument it
  // sends a picked answer from a structured question (the composer is left untouched).
  async function send(answer?: string) {
    const fromComposer = answer === undefined;
    const text = (answer ?? input).trim();
    if (!text || busy) return;
    const next: Msg[] = [...messages, { role: 'user', content: text }];
    onMessages(next);
    if (fromComposer) setInput('');
    setBusy(true);
    try {
      // The model continues the transcript after the trailing "Assistant:". interactive:true lets it
      // reply with a structured question (surfaced on the Result's ask, rendered as clickable options).
      // Attached files (drag / attach / Files-pick) ride along on the shared `inline` path.
      const transcript = next.map((m) => `${m.role === 'user' ? 'User' : 'Assistant'}: ${msgText(m)}`).join('\n\n') + '\n\nAssistant:';
      const data: AigenticRequest = { prompt: transcript, interactive: true, ...pickerFields(picker) };
      if (attached.length) data.inline = attached;
      const out = await runAigentic(api, picker.engine, data);
      onMessages([...next, { role: 'assistant', content: cleanAnswer(out.output), engine: out.engine, model: out.model, ask: out.ask }]);
      setAttached([]);
    } catch (e) {
      ui.toast({ title: 'Chat failed', description: (e as Error).message, variant: 'error' });
    } finally {
      setBusy(false);
    }
  }

  function onKey(e: KeyboardEvent<HTMLTextAreaElement>) {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      void send();
    }
  }

  return (
    <Stack
      gap={3}
      className={`h-full rounded-md ${dragOver ? 'ring-2 ring-accent ring-offset-2 ring-offset-transparent' : ''}`}
      onDragOver={(e) => {
        e.preventDefault();
        if (!dragOver) setDragOver(true);
      }}
      onDragLeave={(e) => {
        if (e.currentTarget === e.target) setDragOver(false);
      }}
      onDrop={onDrop}
    >
      <ScrollArea id={SCROLL_ID} onScroll={saveScroll} className="grow max-h-[55vh] min-h-[28vh] pr-1">
        {messages.length === 0 ? (
          <EmptyState title="Ask anything" description="Pick an engine below — Auto chooses for you — then start chatting." />
        ) : (
          <Stack gap={4}>
            {messages.map((m, i) =>
              m.role === 'user' ? (
                <Box key={i} className="self-end max-w-[85%] rounded-md bg-accent/15 px-3 py-2">
                  <Text className="whitespace-pre-wrap leading-relaxed">{m.content}</Text>
                </Box>
              ) : (
                <Stack key={i} gap={1} className="max-w-full">
                  <Stack direction="row" align="center" gap={2}>
                    <EngineTag engine={m.engine} model={m.model} size="caption" />
                  </Stack>
                  {m.ask && !m.content ? null : <AnswerBody text={m.content} />}
                  {m.ask && m.ask.questions.length > 0 && (
                    <AskChoice
                      questions={m.ask.questions}
                      onAnswer={(t) => void send(t)}
                      disabled={busy || i !== messages.length - 1}
                    />
                  )}
                </Stack>
              ),
            )}
          </Stack>
        )}
        {busy && (
          <Stack direction="row" align="center" gap={2} className="mt-3">
            <Spinner className="h-4 w-4" />
            <Text variant="footnote" color="secondary">
              {live.stage ?? 'Denkt nach …'}
            </Text>
          </Stack>
        )}
      </ScrollArea>

      <Stack gap={2}>
        <EnginePicker p={picker} compact />

        {picker.engine === 'ollama' && live.residency && (
          <Stack direction="row" align="center" gap={2} className="flex-wrap">
            <Badge variant={live.residency.hot ? 'accent' : 'neutral'}>{live.residency.hot ? 'warm' : 'kalt'}</Badge>
            <Text variant="caption" color="tertiary">
              {live.residency.hot && live.residency.loaded
                ? `${live.residency.name} · ${formatBytes(live.residency.loaded.vramBytes)} VRAM · entlädt in ${mmss(
                    live.residency.secondsRemaining,
                  )} · ctx ${live.residency.loaded.contextLength}${live.residency.loaded.fullyOnGpu ? '' : ' · teils CPU'}`
                : `${live.residency.name} · lädt beim nächsten Senden von der SSD`}
            </Text>
          </Stack>
        )}

        {attached.length > 0 && (
          <Stack direction="row" gap={2} align="center" className="flex-wrap">
            {attached.map((p, i) => (
              <Stack key={i} direction="row" align="center" gap={1} className="rounded-full bg-fill/10 pl-2.5 pr-1 py-0.5">
                <Text variant="caption" truncate className="max-w-[12rem]">
                  {baseName(p.path)}
                </Text>
                <Button variant="ghost" size="sm" aria-label="Entfernen" onClick={() => setAttached((a) => a.filter((_, j) => j !== i))}>
                  ×
                </Button>
              </Stack>
            ))}
          </Stack>
        )}

        <Stack direction="row" gap={2} align="center" className="flex-wrap">
          <UploadControl onFiles={(f) => void addFiles(f)} label="Anhängen" />
          <Button variant="secondary" size="sm" iconLeft={<FilesIcon className="h-4 w-4" />} onClick={() => setPickerOpen(true)}>
            Aus Files
          </Button>
        </Stack>

        <Stack direction="row" gap={2} align="end">
          <Stack grow>
            <Textarea
              value={input}
              onChange={(e) => setInput(e.target.value)}
              onKeyDown={onKey}
              rows={2}
              className="w-full"
              placeholder="Message the AI…  (Enter to send, Shift+Enter for a new line — Dateien reinziehen oder anhängen)"
            />
          </Stack>
          <Button variant="primary" loading={busy} disabled={!input.trim()} onClick={() => void send()}>
            Send
          </Button>
        </Stack>
      </Stack>

      {pickerOpen && <FilesPicker api={apiFor('samba')} onClose={() => setPickerOpen(false)} onPick={addParts} />}
    </Stack>
  );
}
