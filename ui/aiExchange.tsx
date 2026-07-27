import { useState, type ReactNode } from 'react';
import {
  Badge,
  Button,
  Markdown,
  Panel,
  Stack,
  Text,
  Textarea,
  useT,
  type FileEntry,
  type FileViewerActionContext,
  type FolderActionContext,
  type ServiceApiClient,
  type ServiceUiBridge,
  type TextPayload,
} from '@holistic/ui';
import { EnginePicker, pickerFields, usePicker } from './EnginePicker';
import { bytesToBase64, classifyEntry, cleanAnswer, encodePath, readEntryInline, type InlinePart } from './aiFiles';
import { CHAT_SEED_KEY, type AigenticRequest, type ChatSeed, type Result, type RunResponse } from './types';

// The shared aigentic "Ask AI" surface. EVERY AI turn in this service — the folder panel, the
// single-file panel and the chat tab — reuses the pieces here rather than re-deriving them:
//   • runAigentic — the ONE backend access point (builds the { header:{kind}, data } envelope);
//   • EngineTag / AnswerBody — the shared "which model answered" + reply renderers;
//   • AskAiExchange — the ONE panel body; the two entry points differ only in how they gather
//     the file bytes, so folder-vs-file is a `gather` closure, not a second panel.

// ── the single backend access point ─────────────────────────────────────────────────────────
// runAigentic is the sole place the run envelope is constructed and posted. `client` is a
// service client already scoped to aigentic (the tab's own `api`, or `apiFor('aigentic')` from a
// Files/Mail host). Callers pass the chosen engine + request body and get the Result back.
export async function runAigentic(client: ServiceApiClient, engine: string, data: AigenticRequest): Promise<Result> {
  const res = await client.post<RunResponse>('run', { header: { kind: engine }, data });
  return res.data;
}

// ── shared answer renderers ─────────────────────────────────────────────────────────────────
// EngineTag is the "which model answered" line (engine badge + model name) shown above every AI
// reply. `size` matches the two hosts' type scales — footnote in the panels, caption in the chat
// bubbles — so attribution looks identical wherever it appears.
export function EngineTag({ engine, model, size = 'footnote' }: { engine?: string; model?: string; size?: 'footnote' | 'caption' }) {
  return (
    <>
      <Badge variant="accent">{engine ?? 'ai'}</Badge>
      {model && (
        <Text variant={size} color={size === 'caption' ? 'tertiary' : 'secondary'}>
          {model}
        </Text>
      )}
    </>
  );
}

// AnswerBody renders a cleaned reply as Markdown, or the shared empty-state placeholder.
export function AnswerBody({ text }: { text: string }) {
  const t = useT();
  return text ? <Markdown text={text} /> : <Text color="secondary">{t('aigentic.emptyResponse')}</Text>;
}

// ── the one Ask-AI panel body ───────────────────────────────────────────────────────────────
// gather collects the file bytes to send and reports its own progress via setNote; it returns the
// inline parts, or null when there is nothing readable (having already toasted why). This closure
// is the ONLY thing that differs between the folder and single-file entry points.
type Gather = (setNote: (s: string) => void) => Promise<InlinePart[] | null>;

interface AskAiExchangeProps {
  apiFor: (serviceId: string) => ServiceApiClient;
  ui: ServiceUiBridge;
  openService: (serviceId: string, subPath?: string) => void;
  close: () => void;
  scopeNote: ReactNode; // host-specific "what will be sent" line
  defaultPrompt: string;
  placeholder: string;
  handoffLabel: string; // ChatSeed.folder — the folder path or file name
  gather: Gather;
}

function AskAiExchange({ apiFor, ui, openService, close, scopeNote, defaultPrompt, placeholder, handoffLabel, gather }: AskAiExchangeProps) {
  const [prompt, setPrompt] = useState(defaultPrompt);
  const picker = usePicker(apiFor);
  const [busy, setBusy] = useState(false);
  const [note, setNote] = useState('');
  const [result, setResult] = useState<Result | null>(null);
  const t = useT();
  const answer = result ? cleanAnswer(result.output) : '';

  async function run() {
    setBusy(true);
    setResult(null);
    setNote('');
    try {
      const parts = await gather(setNote);
      if (!parts || parts.length === 0) return; // gather already explained why (toast)
      const data: AigenticRequest = { prompt, inline: parts, ...pickerFields(picker) };
      setResult(await runAigentic(apiFor('aigentic'), picker.engine, data));
    } catch (e) {
      ui.toast({ title: t('aigentic.runError'), description: (e as Error).message, variant: 'error' });
    } finally {
      setBusy(false);
      setNote('');
    }
  }

  // Hand the exchange off to the full aigentic chat tab, where it continues as a conversation.
  function continueInChat() {
    if (!result) return;
    const seed: ChatSeed = { prompt, answer, engine: result.engine, model: result.model, folder: handoffLabel };
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
        {scopeNote}
      </Text>

      <Textarea value={prompt} onChange={(e) => setPrompt(e.target.value)} rows={3} placeholder={placeholder} />

      <EnginePicker p={picker} />

      <Stack direction="row" gap={2} align="center">
        <Button variant="primary" loading={busy} onClick={run}>
          {t('aigentic.askAi')}
        </Button>
        <Button variant="secondary" onClick={close}>
          {t('common.close')}
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
            <EngineTag engine={result.engine} model={result.model} />
            {result.usage?.truncated && <Badge variant="neutral">{t('aigentic.contextTruncated')}</Badge>}
            <Button variant="secondary" size="sm" onClick={continueInChat}>
              {t('aigentic.continueInChat')}
            </Button>
          </Stack>
          <Panel className="p-4 bg-fill/5">
            <AnswerBody text={answer} />
          </Panel>
        </Stack>
      )}
    </Stack>
  );
}

// ── entry point 1: folder scope (Files toolbar) ─────────────────────────────────────────────
// Bound the payload: Anthropic caps a request at ~32 MB; keep well under it and under a sane file
// count so a huge folder doesn't stall the browser.
const MAX_FILES = 50;
const MAX_TOTAL_BYTES = 25 * 1024 * 1024;

// expand flattens a list of entries, recursing into folders, capped at MAX_FILES.
async function expand(entries: FileEntry[], api: ServiceApiClient, depth = 0): Promise<FileEntry[]> {
  const out: FileEntry[] = [];
  for (const e of entries) {
    if (out.length >= MAX_FILES) break;
    if (e.kind === 'dir') {
      if (depth >= 6) continue;
      try {
        const sub = await api.get<{ entries: FileEntry[] }>(`fs/list?path=${encodePath(e.path)}`);
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

// AskAiFolderPanel asks about a folder / multi-file selection: it traverses the share (recursing
// folders, size-capped) and hands the gathered parts to the shared exchange. Each entry is read
// through the shared readEntryInline (the ONE Holistic-fs reader).
export function AskAiFolderPanel({ cwd, entries, selection, api, apiFor, ui, openService, close }: FolderActionContext) {
  const t = useT();
  const scope = selection.length > 0 ? selection : entries;

  const gather: Gather = async (setNote) => {
    setNote(t('aigentic.note.gathering'));
    const files = await expand(scope, api);
    if (files.length === 0) {
      ui.toast({ title: t('aigentic.note.noFiles'), variant: 'error' });
      return null;
    }
    const parts: InlinePart[] = [];
    let total = 0;
    let read = 0;
    for (const e of files) {
      if (total >= MAX_TOTAL_BYTES) break;
      const part = await readEntryInline(api, e);
      if (!part) continue;
      parts.push(part);
      total += part.content.length;
      if (part.content) read += 1;
    }
    if (parts.length === 0) {
      ui.toast({ title: t('aigentic.note.noneRead'), variant: 'error' });
      return null;
    }
    setNote(t('aigentic.note.sending', { read, total: parts.length }));
    return parts;
  };

  return (
    <AskAiExchange
      apiFor={apiFor}
      ui={ui}
      openService={openService}
      close={close}
      scopeNote={
        selection.length > 0
          ? t('aigentic.scope.folderSelected', { cwd, count: selection.length })
          : t('aigentic.scope.folderAll', { cwd })
      }
      defaultPrompt={t('aigentic.prompt.folder')}
      placeholder={t('aigentic.placeholder.folder')}
      handoffLabel={cwd}
      gather={gather}
    />
  );
}

// ── entry point 2: single file (shared FilePreview) ─────────────────────────────────────────
// buildFilePart turns the shown file into one inline part from host-provided content — text
// inline, PDFs/web images as base64. It shares the SAME text/image/pdf rule as everywhere else
// (classifyEntry); only the byte source differs (the host already loaded the preview, so there is
// no fs read here). Returns null when the file can't be read.
async function buildFilePart(entry: FileEntry, text: TextPayload | null | undefined, loadBytes?: () => Promise<Uint8Array>): Promise<InlinePart | null> {
  const { kind, mediaType } = classifyEntry(entry);
  if (kind === 'text') {
    return text?.content ? { path: entry.name, content: text.content, mediaType: '' } : null;
  }
  if ((kind === 'image' || kind === 'pdf') && loadBytes) {
    return { path: entry.name, content: bytesToBase64(await loadBytes()), mediaType };
  }
  return null;
}

// AskAiFilePanel asks about the ONE displayed file, mounted in the shared FilePreview (Files + Mail
// attachments). It does NO fileshare traversal: the host already loaded the file to preview it, so
// text arrives in `text` and binary bytes come from the host's `loadBytes`.
export function AskAiFilePanel({ entry, text, loadBytes, apiFor, ui, openService, close }: FileViewerActionContext) {
  const t = useT();
  const gather: Gather = async (setNote) => {
    setNote(t('aigentic.note.reading'));
    const part = await buildFilePart(entry, text, loadBytes);
    if (!part) {
      ui.toast({ title: t('aigentic.note.fileUnreadable'), variant: 'error' });
      return null;
    }
    setNote(t('aigentic.note.asking'));
    return [part];
  };

  return (
    <AskAiExchange
      apiFor={apiFor}
      ui={ui}
      openService={openService}
      close={close}
      scopeNote={t('aigentic.scope.file', { name: entry.name })}
      defaultPrompt={t('aigentic.prompt.file')}
      placeholder={t('aigentic.placeholder.file')}
      handoffLabel={entry.name}
      gather={gather}
    />
  );
}
