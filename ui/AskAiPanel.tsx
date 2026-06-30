import { useMemo, useState, type ReactNode } from 'react';
import {
  Badge,
  Button,
  CodeBlock,
  Panel,
  Stack,
  Text,
  Textarea,
  type FileEntry,
  type FolderActionContext,
  type TextPayload,
} from '@holistic/ui';
import type { AigenticRequest, RunResponse } from './types';

// Keep payloads modest: aigentic's context budget is 64 KiB and the HTTP body cap is 1 MiB,
// so cap the file count and total bytes the browser bundles from the folder.
const MAX_FILES = 20;
const MAX_TOTAL_BYTES = 200 * 1024;

// Files whose content the Files backend can serve as text (fs/text). Binaries/media are
// skipped — aigentic only takes text context.
function isReadableText(e: FileEntry): boolean {
  return e.kind === 'file' && (e.viewer === 'text' || e.viewer === 'markdown');
}

// --- minimal, dependency-free Markdown renderer ----------------------------------------
// Service UIs may not render raw HTML, so this composes @holistic/ui primitives only. It
// turns the model's plaintext/markdown answer into headings, lists, code and emphasis.

// Strip the <file path="…">…</file> context wrappers a weak local model sometimes echoes back,
// and collapse excess blank lines.
function cleanOutput(s: string): string {
  return s
    .replace(/<\/?file\b[^>]*>/g, '')
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
      i++; // consume the closing fence
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

// inline parses **bold** and `code` spans into nodes (no raw HTML; Text spans only).
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

// AskAiPanel is aigentic's folder-level action, rendered INSIDE the Files app. It reads the
// folder's text files through the Files app's own (Samba) client — so the user's private 0700
// space stays confined to the existing, privileged Files backend — then sends the bytes inline
// to aigentic's /run via the cross-service client. aigentic never touches the filesystem.
export function AskAiPanel({ cwd, entries, selection, api, apiFor, ui, close }: FolderActionContext) {
  const [prompt, setPrompt] = useState('Summarize these files.');
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<RunResponse['data'] | null>(null);

  // Target the selection if any files are selected, else every readable file in the folder.
  const targets = useMemo(() => {
    const picked = selection.filter(isReadableText);
    const pool = picked.length > 0 ? picked : entries.filter(isReadableText);
    return pool.slice(0, MAX_FILES);
  }, [selection, entries]);

  const skipped = (selection.length > 0 ? selection : entries).filter((e) => e.kind === 'file' && !isReadableText(e)).length;
  const answer = result ? cleanOutput(result.output) : '';

  async function run() {
    if (targets.length === 0) {
      ui.toast({ title: 'No readable text files here', variant: 'error' });
      return;
    }
    setBusy(true);
    setResult(null);
    try {
      const inlineFiles: { path: string; content: string }[] = [];
      let total = 0;
      for (const e of targets) {
        if (total >= MAX_TOTAL_BYTES) break;
        try {
          const payload = await api.get<TextPayload>(`fs/text?path=${encodeURIComponent(e.path)}`);
          if (payload?.content) {
            inlineFiles.push({ path: e.path, content: payload.content });
            total += payload.content.length;
          }
        } catch {
          // Unreadable file — skip it rather than fail the whole request.
        }
      }
      if (inlineFiles.length === 0) {
        ui.toast({ title: 'Could not read any file in this folder', variant: 'error' });
        return;
      }
      const data: AigenticRequest = { prompt, inline: inlineFiles };
      // choose routes by complexity; the cross-service client carries the same session + CSRF.
      const res = await apiFor('aigentic').post<RunResponse>('run', { header: { kind: 'choose' }, data });
      setResult(res.data);
    } catch (e) {
      ui.toast({ title: 'AI request failed', description: (e as Error).message, variant: 'error' });
    } finally {
      setBusy(false);
    }
  }

  return (
    <Stack gap={3}>
      <Text variant="footnote" color="secondary">
        Folder “{cwd}” — {targets.length} text file{targets.length === 1 ? '' : 's'} will be sent
        {skipped > 0 ? ` (${skipped} non-text skipped)` : ''}.
      </Text>
      <Textarea value={prompt} onChange={(e) => setPrompt(e.target.value)} rows={3} placeholder="Ask the AI about these files…" />
      <Stack direction="row" gap={2}>
        <Button variant="primary" loading={busy} disabled={targets.length === 0} onClick={run}>
          Ask AI
        </Button>
        <Button variant="secondary" onClick={close}>
          Close
        </Button>
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
