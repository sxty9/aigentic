import { useEffect, useLayoutEffect, useRef, useState, type KeyboardEvent } from 'react';
import {
  Badge,
  Box,
  Button,
  EmptyState,
  Markdown,
  ScrollArea,
  Spinner,
  Stack,
  Text,
  Textarea,
  type ServiceApiClient,
  type ServiceContextProps,
} from '@holistic/ui';
import { EnginePicker, pickerFields, type Picker } from './EnginePicker';
import { clean, type Msg } from './chatStore';
import type { AigenticRequest, RunResponse } from './types';

// Per-chat scroll memory (session-scoped, keyed by chat id): returning to a chat restores where
// you last were, while a new message jumps to the bottom.
const scrollMem = new Map<string, number>();
const SCROLL_ID = 'aigentic-chat-scroll';
const scrollEl = () => document.getElementById(SCROLL_ID);

// ChatView is the conversation pane for one chat. It is controlled: the message list lives in the
// chat store (so it persists + drives the sidebar), and the picker is owned by the parent (so the
// engine choice survives switching chats). Multi-turn is stateless on the backend — the whole
// conversation is sent as one transcript prompt to /run, inheriting per-user creds, Auto routing
// and multimodal for free.
export function ChatView({
  chatId,
  api,
  ui,
  picker,
  messages,
  onMessages,
}: {
  chatId: string;
  api: ServiceApiClient;
  ui: ServiceContextProps['ui'];
  picker: Picker;
  messages: Msg[];
  onMessages: (m: Msg[]) => void;
}) {
  const [input, setInput] = useState('');
  const [busy, setBusy] = useState(false);
  const prevLen = useRef(messages.length);

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

  // Keep the "Thinking…" row in view while a reply is pending.
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

  async function send() {
    const text = input.trim();
    if (!text || busy) return;
    const next: Msg[] = [...messages, { role: 'user', content: text }];
    onMessages(next);
    setInput('');
    setBusy(true);
    try {
      // The model continues the transcript after the trailing "Assistant:".
      const transcript = next.map((m) => `${m.role === 'user' ? 'User' : 'Assistant'}: ${m.content}`).join('\n\n') + '\n\nAssistant:';
      const data: AigenticRequest = { prompt: transcript, ...pickerFields(picker) };
      const res = await api.post<RunResponse>('run', { header: { kind: picker.engine }, data });
      onMessages([...next, { role: 'assistant', content: clean(res.data.output), engine: res.data.engine, model: res.data.model }]);
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
    <Stack gap={3} className="h-full">
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
                    <Badge variant="accent">{m.engine ?? 'ai'}</Badge>
                    {m.model && (
                      <Text variant="caption" color="tertiary">
                        {m.model}
                      </Text>
                    )}
                  </Stack>
                  {m.content ? <Markdown text={m.content} /> : <Text color="secondary">(empty response)</Text>}
                </Stack>
              ),
            )}
          </Stack>
        )}
        {busy && (
          <Stack direction="row" align="center" gap={2} className="mt-3">
            <Spinner className="h-4 w-4" />
            <Text variant="footnote" color="secondary">
              Thinking…
            </Text>
          </Stack>
        )}
      </ScrollArea>

      <Stack gap={2}>
        <EnginePicker p={picker} compact />
        <Stack direction="row" gap={2} align="end">
          <Stack grow>
            <Textarea
              value={input}
              onChange={(e) => setInput(e.target.value)}
              onKeyDown={onKey}
              rows={2}
              className="w-full"
              placeholder="Message the AI…  (Enter to send, Shift+Enter for a new line)"
            />
          </Stack>
          <Button variant="primary" loading={busy} disabled={!input.trim()} onClick={send}>
            Send
          </Button>
        </Stack>
      </Stack>
    </Stack>
  );
}
