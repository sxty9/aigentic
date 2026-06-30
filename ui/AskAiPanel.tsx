import { useMemo, useState, type ReactNode } from 'react';
import {
  Badge,
  Button,
  CodeBlock,
  Panel,
  SegmentedControl,
  Stack,
  Text,
  Textarea,
  type FileEntry,
  type FolderActionContext,
  type ServiceApiClient,
  type TextPayload,
} from '@holistic/ui';
import type { AigenticRequest, RunResponse } from './types';

// Bound the payload: Anthropic caps a request at ~32 MB; keep well under it and under a sane
// file count so a huge folder doesn't stall the browser.
const MAX_FILES = 50;
const MAX_TOTAL_BYTES = 25 * 1024 * 1024;

const q = (p: string) => encodeURIComponent(p);

// --- providers / models / effort (the picker) ---------------------------------------------
const PROVIDERS = [
  { value: 'choose', label: 'Auto' },
  { value: 'ollama', label: 'Local' },
  { value: 'claude-cli', label: 'Claude CLI' },
  { value: 'claude-api', label: 'Claude API' },
] as const;
const CLAUDE_MODELS = [
  { value: '', label: 'Default' },
  { value: 'claude-sonnet-4-6', label: 'Sonnet' },
  { value: 'claude-opus-4-8', label: 'Opus' },
  { value: 'claude-haiku-4-5', label: 'Haiku' },
] as const;
const EFFORTS = [
  { value: '', label: 'Default' },
  { value: 'low', label: 'Low' },
  { value: 'medium', label: 'Med' },
  { value: 'high', label: 'High' },
] as const;
const usesClaude = (p: string) => p === 'claude-cli' || p === 'claude-api' || p === 'choose';

// --- gather the folder's files (recursing folders), as inline parts ------------------------

// fetchBase64 reads raw bytes via the Files app's own client and base64-encodes them in the
// browser (chunked, to avoid call-stack limits on large files).
async function fetchBase64(api: ServiceApiClient, path: string): Promise<string> {
  const res = await api.raw(`fs/raw?path=${q(path)}`);
  const bytes = new Uint8Array(await res.arrayBuffer());
  let binary = '';
  const chunk = 0x8000;
  for (let i = 0; i < bytes.length; i += chunk) {
    binary += String.fromCharCode(...bytes.subarray(i, i + chunk));
  }
  return btoa(binary);
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

type InlinePart = { path: string; content: string; mediaType?: string };

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

// --- minimal Markdown renderer (no raw HTML; @holistic/ui primitives only) -----------------
function cleanOutput(s: string): string {
  return s
    .replace(/<\/?(file|attachment)\b[^>]*>/g, '')
    .replace(/\n{3,}/g, '\n\n')
    .trim();
}
type Block =
  | { kind: 'code'; code: string }
  | { kind: 'heading'; level: number; text: string }
  | { kind: 'list'; items: string[] }
  | { kind: 'para'; text: string };
const LIST_RE = /^\s*([-*•]|\d+[.)])\s+/;
const HEAD_RE = /^(#{1,4})\s+(.*)$/;
function parseBlocks(src: string): Block[] {
  const lines = src.split('\n');
  const blocks: Block[] = [];
  let i = 0;
  while (i < lines.length) {
    const line = lines[i];
    if (line.trim().startsWith('```')) {
      const buf: string[] = [];
      i++;
      while (i < lines.length && !lines[i].trim().startsWith('```')) {
        buf.push(lines[i]);
        i++;
      }
      i++;
      blocks.push({ kind: 'code', code: buf.join('\n') });
      continue;
    }
    const h = HEAD_RE.exec(line);
    if (h) {
      blocks.push({ kind: 'heading', level: h[1].length, text: h[2] });
      i++;
      continue;
    }
    if (LIST_RE.test(line)) {
      const items: string[] = [];
      while (i < lines.length && LIST_RE.test(lines[i])) {
        items.push(lines[i].replace(LIST_RE, ''));
        i++;
      }
      blocks.push({ kind: 'list', items });
      continue;
    }
    if (line.trim() === '') {
      i++;
      continue;
    }
    const buf: string[] = [];
    while (i < lines.length && lines[i].trim() !== '' && !HEAD_RE.test(lines[i]) && !LIST_RE.test(lines[i]) && !lines[i].trim().startsWith('```')) {
      buf.push(lines[i]);
      i++;
    }
    blocks.push({ kind: 'para', text: buf.join(' ') });
  }
  return blocks;
}
function inline(s: string): ReactNode[] {
  const out: ReactNode[] = [];
  const re = /(\*\*([^*]+)\*\*|`([^`]+)`)/g;
  let last = 0;
  let key = 0;
  let m: RegExpExecArray | null;
  while ((m = re.exec(s)) !== null) {
    if (m.index > last) out.push(s.slice(last, m.index));
    if (m[2] != null) {
      out.push(
        <Text key={key++} as="span" weight="semibold">
          {m[2]}
        </Text>,
      );
    } else {
      out.push(
        <Text key={key++} as="code" className="rounded bg-fill/20 px-1 py-0.5 font-mono text-footnote">
          {m[3]}
        </Text>,
      );
    }
    last = m.index + m[0].length;
  }
  if (last < s.length) out.push(s.slice(last));
  return out;
}
function Markdown({ text }: { text: string }) {
  const blocks = useMemo(() => parseBlocks(text), [text]);
  return (
    <Stack gap={2}>
      {blocks.map((b, i) => {
        if (b.kind === 'code') return <CodeBlock key={i} code={b.code} />;
        if (b.kind === 'heading') {
          return (
            <Text key={i} variant={b.level === 1 ? 'title3' : b.level === 2 ? 'subhead' : 'body'} weight="semibold">
              {inline(b.text)}
            </Text>
          );
        }
        if (b.kind === 'list') {
          return (
            <Stack key={i} gap={1}>
              {b.items.map((it, j) => (
                <Text key={j} className="leading-relaxed">
                  <Text as="span" color="secondary">
                    {'•  '}
                  </Text>
                  {inline(it)}
                </Text>
              ))}
            </Stack>
          );
        }
        return (
          <Text key={i} className="leading-relaxed">
            {inline(b.text)}
          </Text>
        );
      })}
    </Stack>
  );
}

// --- the panel -----------------------------------------------------------------------------
export function AskAiPanel({ cwd, entries, selection, api, apiFor, ui, close }: FolderActionContext) {
  const [prompt, setPrompt] = useState('Summarize these files.');
  const [provider, setProvider] = useState<string>('choose');
  const [model, setModel] = useState<string>('');
  const [effort, setEffort] = useState<string>('');
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
      const data: AigenticRequest = { prompt, inline: inlineFiles };
      if (model) data.model = model;
      if (effort) data.claude = { effort };
      const res = await apiFor('aigentic').post<RunResponse>('run', { header: { kind: provider }, data });
      setResult(res.data);
    } catch (e) {
      ui.toast({ title: 'AI request failed', description: (e as Error).message, variant: 'error' });
    } finally {
      setBusy(false);
      setNote('');
    }
  }

  return (
    <Stack gap={3}>
      <Text variant="footnote" color="secondary">
        Folder “{cwd}” — {selection.length > 0 ? `${selection.length} selected item(s)` : 'all items'} (folders are
        included recursively; images &amp; PDFs are read by Claude models, other files are listed).
      </Text>

      <Textarea value={prompt} onChange={(e) => setPrompt(e.target.value)} rows={3} placeholder="Ask the AI about these files…" />

      <Stack direction="row" gap={3} align="center" className="flex-wrap">
        <Stack gap={1}>
          <Text variant="caption" color="tertiary">
            Engine
          </Text>
          <SegmentedControl value={provider} onChange={setProvider} options={PROVIDERS.map((p) => ({ value: p.value, label: p.label }))} />
        </Stack>
        {usesClaude(provider) && (
          <Stack gap={1}>
            <Text variant="caption" color="tertiary">
              Model
            </Text>
            <SegmentedControl value={model} onChange={setModel} options={CLAUDE_MODELS.map((m) => ({ value: m.value, label: m.label }))} />
          </Stack>
        )}
        {usesClaude(provider) && (
          <Stack gap={1}>
            <Text variant="caption" color="tertiary">
              Effort
            </Text>
            <SegmentedControl value={effort} onChange={setEffort} options={EFFORTS.map((e) => ({ value: e.value, label: e.label }))} />
          </Stack>
        )}
      </Stack>

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
          <Stack direction="row" align="center" gap={2}>
            <Badge variant="accent">{result.engine ?? 'ai'}</Badge>
            {result.model && (
              <Text variant="footnote" color="secondary">
                {result.model}
              </Text>
            )}
            {result.usage?.truncated && <Badge variant="neutral">context truncated</Badge>}
          </Stack>
          <Panel className="p-4 bg-fill/5">
            {answer ? <Markdown text={answer} /> : <Text color="secondary">(empty response)</Text>}
          </Panel>
        </Stack>
      )}
    </Stack>
  );
}
