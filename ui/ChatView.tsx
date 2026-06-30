import { useEffect, useRef, useState, type KeyboardEvent } from 'react';
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
  type ServiceContextProps,
} from '@holistic/ui';
import { EnginePicker, pickerFields, usePicker } from './EnginePicker';
import { CHAT_SEED_KEY, type AigenticRequest, type ChatSeed, type RunResponse } from './types';

interface Msg {
  role: 'user' | 'assistant';
  content: string;
  engine?: string;
  model?: string;
}

// clean strips the leading "Assistant:" the transcript framing can echo, plus any file-context
// tags carried over from a seeded handoff.
function clean(s: string): string {
  return s
    .replace(/^\s*Assistant:\s*/i, '')
    .replace(/<\/?(file|attachment)\b[^>]*>/g, '')
    .trim();
}

// ChatView is the aigentic tab's conversational surface (Perplexity-style): pick an engine /
// model (or Auto) and chat. Multi-turn is stateless on the backend — the whole conversation is
// sent as one transcript prompt to /run, so the chat inherits the per-user credentials, the
// Auto router and multimodal for free. A "continue in chat" handoff from the Files "Ask AI"
// dialog seeds the first exchange via localStorage.
export function ChatView({ api, apiFor, ui }: Pick<ServiceContextProps, 'api' | 'apiFor' | 'ui'>) {
  const picker = usePicker(apiFor);
  const [messages, setMessages] = useState<Msg[]>([]);
  const [input, setInput] = useState('');
  const [busy, setBusy] = useState(false);
  const seeded = useRef(false);

  // Pick up a "continue in chat" handoff from the Files app (once), then clear it.
  useEffect(() => {
    if (seeded.current) return;
    seeded.current = true;
    try {
      const raw = localStorage.getItem(CHAT_SEED_KEY);
      if (!raw) return;
      localStorage.removeItem(CHAT_SEED_KEY);
      const seed = JSON.parse(raw) as ChatSeed;
      if (seed?.prompt || seed?.answer) {
        setMessages([
          { role: 'user', content: seed.prompt || '(files)' },
          { role: 'assistant', content: clean(seed.answer || ''), engine: seed.engine, model: seed.model },
        ]);
      }
    } catch {
      // malformed seed — ignore, start an empty chat
    }
  }, []);

  // Keep the latest turn in view as the conversation grows.
  useEffect(() => {
    document.getElementById('aigentic-chat-end')?.scrollIntoView({ block: 'end', behavior: 'smooth' });
  }, [messages, busy]);

  async function send() {
    const text = input.trim();
    if (!text || busy) return;
    const next: Msg[] = [...messages, { role: 'user', content: text }];
    setMessages(next);
    setInput('');
    setBusy(true);
    try {
      // The model continues the transcript after the trailing "Assistant:".
      const transcript = next.map((m) => `${m.role === 'user' ? 'User' : 'Assistant'}: ${m.content}`).join('\n\n') + '\n\nAssistant:';
      const data: AigenticRequest = { prompt: transcript, ...pickerFields(picker) };
      const res = await api.post<RunResponse>('run', { header: { kind: picker.engine }, data });
      setMessages((m) => [...m, { role: 'assistant', content: clean(res.data.output), engine: res.data.engine, model: res.data.model }]);
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
    <Stack gap={3}>
      <Stack direction="row" align="center" justify="between">
        <Text variant="subhead" weight="semibold">
          Chat
        </Text>
        <Button size="sm" variant="ghost" disabled={!messages.length || busy} onClick={() => setMessages([])}>
          New chat
        </Button>
      </Stack>

      <ScrollArea className="max-h-[55vh] min-h-[28vh] pr-1">
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
        <Box id="aigentic-chat-end" />
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
