import { useState } from 'react';
import {
  Badge,
  Button,
  Markdown,
  Panel,
  Stack,
  Text,
  Textarea,
  type FileViewerActionContext,
} from '@holistic/ui';
import { EnginePicker, pickerFields, usePicker } from './EnginePicker';
import { bytesToBase64, cleanOutput, type InlinePart } from './aiFiles';
import { CHAT_SEED_KEY, type AigenticRequest, type ChatSeed, type RunResponse } from './types';

// The single-file "Ask AI" mounted in the shared FilePreview (Files + Mail attachments). Unlike the
// folder panel it does NO fileshare traversal: the host already loaded the file to preview it, so
// text arrives in `text` and binary bytes come from the host's `loadBytes`. The engine/model picker,
// request shape, result rendering and chat handoff are identical to the folder panel.
export function AskAiFilePanel({ entry, text, loadBytes, apiFor, ui, openService, close }: FileViewerActionContext) {
  const [prompt, setPrompt] = useState('Summarize this file.');
  const picker = usePicker(apiFor);
  const [busy, setBusy] = useState(false);
  const [note, setNote] = useState('');
  const [result, setResult] = useState<RunResponse['data'] | null>(null);
  const answer = result ? cleanOutput(result.output) : '';

  // buildPart turns the shown file into a single inline part from host-provided content — text
  // inline, PDFs/web images as base64. Returns null when the file can't be read (no button should
  // reach here, since `applies`/aiReadable already gates that, but stay defensive).
  async function buildPart(): Promise<InlinePart | null> {
    if (entry.viewer === 'text' || entry.viewer === 'markdown') {
      return text?.content ? { path: entry.name, content: text.content, mediaType: '' } : null;
    }
    const mime = entry.mime ?? '';
    const isPdf = entry.viewer === 'pdf' || mime === 'application/pdf';
    const isWebImage = entry.viewer === 'image' && /^image\/(png|jpeg|gif|webp)$/.test(mime);
    if ((isPdf || isWebImage) && loadBytes) {
      const bytes = await loadBytes();
      return { path: entry.name, content: bytesToBase64(bytes), mediaType: isPdf ? 'application/pdf' : mime };
    }
    return null;
  }

  async function run() {
    setBusy(true);
    setResult(null);
    setNote('Reading file…');
    try {
      const part = await buildPart();
      if (!part) {
        ui.toast({ title: 'This file can’t be read by the AI', variant: 'error' });
        return;
      }
      setNote('Asking the AI…');
      const data: AigenticRequest = { prompt, inline: [part], ...pickerFields(picker) };
      const res = await apiFor('aigentic').post<RunResponse>('run', { header: { kind: picker.engine }, data });
      setResult(res.data);
    } catch (e) {
      ui.toast({ title: 'AI request failed', description: (e as Error).message, variant: 'error' });
    } finally {
      setBusy(false);
      setNote('');
    }
  }

  // Hand the exchange off to the full aigentic chat tab, where it continues as a conversation.
  function continueInChat() {
    if (!result) return;
    const seed: ChatSeed = { prompt, answer, engine: result.engine, model: result.model, folder: entry.name };
    try {
      localStorage.setItem(CHAT_SEED_KEY, JSON.stringify(seed));
    } catch {
      // localStorage unavailable (private mode / quota) — fall back to opening an empty chat.
    }
    openService('aigentic');
    close();
  }

  return (
    <Stack gap={3}>
      <Text variant="footnote" color="secondary">
        “{entry.name}” — text is read inline; images &amp; PDFs are read by Claude models.
      </Text>

      <Textarea value={prompt} onChange={(e) => setPrompt(e.target.value)} rows={3} placeholder="Ask the AI about this file…" />

      <EnginePicker p={picker} />

      <Stack direction="row" gap={2} align="center">
        <Button variant="primary" loading={busy} onClick={run}>
          Ask AI
        </Button>
        <Button variant="secondary" onClick={close}>
          Close
        </Button>
        {note && (
          <Text variant="footnote" color="secondary">
            {note}
          </Text>
        )}
      </Stack>

      {result && (
        <Stack gap={2}>
          <Stack direction="row" align="center" gap={2} className="flex-wrap">
            <Badge variant="accent">{result.engine ?? 'ai'}</Badge>
            {result.model && (
              <Text variant="footnote" color="secondary">
                {result.model}
              </Text>
            )}
            {result.usage?.truncated && <Badge variant="neutral">context truncated</Badge>}
            <Button variant="secondary" size="sm" onClick={continueInChat}>
              Continue in chat →
            </Button>
          </Stack>
          <Panel className="p-4 bg-fill/5">
            {answer ? <Markdown text={answer} /> : <Text color="secondary">(empty response)</Text>}
          </Panel>
        </Stack>
      )}
    </Stack>
  );
}
