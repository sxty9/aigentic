import { useMemo, useState } from 'react';
import {
  Badge,
  Button,
  Markdown,
  Panel,
  Stack,
  Text,
  Textarea,
  type FileEntry,
  type FolderActionContext,
  type ServiceApiClient,
  type TextPayload,
} from '@holistic/ui';
import { EnginePicker, pickerFields, usePicker } from './EnginePicker';
import { bytesToBase64, cleanOutput, type InlinePart } from './aiFiles';
import { CHAT_SEED_KEY, type AigenticRequest, type ChatSeed, type RunResponse } from './types';

// Bound the payload: Anthropic caps a request at ~32 MB; keep well under it and under a sane
// file count so a huge folder doesn't stall the browser.
const MAX_FILES = 50;
const MAX_TOTAL_BYTES = 25 * 1024 * 1024;

const q = (p: string) => encodeURIComponent(p);

// --- gather the folder's files (recursing folders), as inline parts ------------------------

// fetchBase64 reads raw bytes via the Files app's own client and base64-encodes them.
async function fetchBase64(api: ServiceApiClient, path: string): Promise<string> {
  const res = await api.raw(`fs/raw?path=${q(path)}`);
  return bytesToBase64(new Uint8Array(await res.arrayBuffer()));
}

// expand flattens a list of entries, recursing into folders, capped at MAX_FILES.
async function expand(entries: FileEntry[], api: ServiceApiClient, depth = 0): Promise<FileEntry[]> {
  const out: FileEntry[] = [];
  for (const e of entries) {
    if (out.length >= MAX_FILES) break;
    if (e.kind === 'dir') {
      if (depth >= 6) continue;
      try {
        const sub = await api.get<{ entries: FileEntry[] }>(`fs/list?path=${q(e.path)}`);
        out.push(...(await expand(sub.entries, api, depth + 1)));
      } catch {
        // unreadable folder — skip
      }
    } else {
      out.push(e);
    }
  }
  return out.slice(0, MAX_FILES);
}

// toInline turns a file into an inline part: text → content; image/PDF → base64 + mediaType;
// anything else → a name-only entry so the AI still "counts" it.
async function toInline(api: ServiceApiClient, e: FileEntry): Promise<InlinePart | null> {
  try {
    if (e.viewer === 'text' || e.viewer === 'markdown') {
      const p = await api.get<TextPayload>(`fs/text?path=${q(e.path)}`);
      return p?.content ? { path: e.path, content: p.content, mediaType: '' } : null;
    }
    if (e.viewer === 'image' && e.mime && /^image\/(png|jpeg|gif|webp)$/.test(e.mime)) {
      return { path: e.path, content: await fetchBase64(api, e.path), mediaType: e.mime };
    }
    if (e.viewer === 'pdf' || e.mime === 'application/pdf') {
      return { path: e.path, content: await fetchBase64(api, e.path), mediaType: 'application/pdf' };
    }
    // Other types: counted only (named in the prompt by the backend), not read.
    return { path: e.path, content: '', mediaType: e.mime || 'application/octet-stream' };
  } catch {
    return null;
  }
}

// --- the panel -----------------------------------------------------------------------------
export function AskAiPanel({ cwd, entries, selection, api, apiFor, ui, openService, close }: FolderActionContext) {
  const [prompt, setPrompt] = useState('Summarize these files.');
  const picker = usePicker(apiFor);
  const [busy, setBusy] = useState(false);
  const [note, setNote] = useState('');
  const [result, setResult] = useState<RunResponse['data'] | null>(null);

  // Scope: the selection if anything is selected, else the whole current folder.
  const scope = useMemo(() => (selection.length > 0 ? selection : entries), [selection, entries]);
  const answer = result ? cleanOutput(result.output) : '';

  async function run() {
    setBusy(true);
    setResult(null);
    setNote('Gathering files…');
    try {
      const files = await expand(scope, api);
      if (files.length === 0) {
        ui.toast({ title: 'No files in this folder', variant: 'error' });
        return;
      }
      const inlineFiles: InlinePart[] = [];
      let total = 0;
      let read = 0;
      for (const e of files) {
        if (total >= MAX_TOTAL_BYTES) break;
        const part = await toInline(api, e);
        if (!part) continue;
        inlineFiles.push(part);
        total += part.content.length;
        if (part.content) read += 1;
      }
      if (inlineFiles.length === 0) {
        ui.toast({ title: 'Could not read any file', variant: 'error' });
        return;
      }
      setNote(`Sending ${read} file${read === 1 ? '' : 's'} (${inlineFiles.length} total) to the AI…`);
      const data: AigenticRequest = { prompt, inline: inlineFiles, ...pickerFields(picker) };
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
    const seed: ChatSeed = { prompt, answer, engine: result.engine, model: result.model, folder: cwd };
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
        Folder “{cwd}” — {selection.length > 0 ? `${selection.length} selected item(s)` : 'all items'} (folders are
        included recursively; images &amp; PDFs are read by Claude models, other files are listed).
      </Text>

      <Textarea value={prompt} onChange={(e) => setPrompt(e.target.value)} rows={3} placeholder="Ask the AI about these files…" />

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
