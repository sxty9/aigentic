import { useMemo, useState } from 'react';
import {
  Badge,
  Button,
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

  async function run() {
    if (targets.length === 0) {
      ui.toast({ title: 'No readable text files here', variant: 'error' });
      return;
    }
    setBusy(true);
    setResult(null);
    try {
      const inline: { path: string; content: string }[] = [];
      let total = 0;
      for (const e of targets) {
        if (total >= MAX_TOTAL_BYTES) break;
        try {
          const payload = await api.get<TextPayload>(`fs/text?path=${encodeURIComponent(e.path)}`);
          if (payload?.content) {
            inline.push({ path: e.path, content: payload.content });
            total += payload.content.length;
          }
        } catch {
          // Unreadable file — skip it rather than fail the whole request.
        }
      }
      if (inline.length === 0) {
        ui.toast({ title: 'Could not read any file in this folder', variant: 'error' });
        return;
      }
      const data: AigenticRequest = { prompt, inline };
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
      <Text color="secondary">
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
            {result.model && <Text color="secondary">{result.model}</Text>}
            {result.usage?.truncated && <Badge variant="neutral">context truncated</Badge>}
          </Stack>
          <Text>{result.output}</Text>
        </Stack>
      )}
    </Stack>
  );
}
