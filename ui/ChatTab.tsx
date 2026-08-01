import { useCallback, useEffect, useMemo, useRef, useState, type DragEvent } from 'react';
import {
  Badge,
  Button,
  Chat,
  DropdownMenu,
  FilesIcon,
  IconButton,
  Spinner,
  Stack,
  Text,
  UploadControl,
  type MenuItem,
  type ServiceContextProps,
} from '@holisdk/ui';
import { makeAigenticAdapter, type ChatExtras } from './chatAdapter';
import { EFFORTS } from './EnginePicker';
import { FilesPicker } from './FilesPicker';
import { fileToInline, type InlinePart } from './aiFiles';
import { CHAT_SEED_KEY, type ChatSeed } from './types';

// ChatTab is the aigentic tab's chat surface. It is NO LONGER a bespoke chat — it drives the ONE
// shared <Chat> building block from @holisdk/ui with an aigentic ChatAdapter. The shared chat owns
// the whole experience (many conversations with switch/create/delete, machine+model choice,
// model-tagged answers, wrapping prose, a transcript and composer); aigentic contributes only its
// OWN parts: the backend (adapter), file attachments and the Claude effort level (composer
// accessory + drag-drop), and the Files "Ask AI" → chat handoff.

// Which conversation is open is remembered per device so a reload reopens the same one
// (Zustandserhalt). We keep selection CONTROLLED (rather than the shared chat's built-in
// persistKey) because the Files handoff must be able to force a freshly-seeded conversation open.
const ACTIVE_KEY = 'aigentic.chat.active';

// Attachment bounds: keep well under the backend's /run cap and a sane file count so a stray
// folder-drop doesn't stall the browser.
const MAX_ATTACH = 25;
const MAX_ATTACH_BYTES = 24 * 1024 * 1024;

const baseName = (p: string) => p.split('/').pop() || p;

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

export function ChatTab({ api, apiFor, ui }: Pick<ServiceContextProps, 'api' | 'apiFor' | 'ui'>) {
  const [attached, setAttached] = useState<InlinePart[]>([]);
  const [effort, setEffort] = useState('');
  const [pickerOpen, setPickerOpen] = useState(false);
  const [dragOver, setDragOver] = useState(false);
  const [activeId, setActiveId] = useState<string | null>(() => {
    try {
      return localStorage.getItem(ACTIVE_KEY);
    } catch {
      return null;
    }
  });
  const [ready, setReady] = useState(false);

  // The adapter reads the live attachments + effort at send time; keep a ref so the memoised
  // adapter never goes stale as those change.
  const extrasRef = useRef<ChatExtras>({ inline: attached, effort });
  extrasRef.current = { inline: attached, effort };

  const clearAttached = useCallback(() => setAttached([]), []);
  const adapter = useMemo(
    () => makeAigenticAdapter(api, () => extrasRef.current, clearAttached),
    [api, clearAttached],
  );

  const selectActive = useCallback((id: string | null) => {
    setActiveId(id);
    try {
      if (id) localStorage.setItem(ACTIVE_KEY, id);
      else localStorage.removeItem(ACTIVE_KEY);
    } catch {
      // storage unavailable (private mode / quota) — selection just isn't remembered across reloads
    }
  }, []);

  // On mount: a Files "Ask AI" handoff (seed) opens a fresh, pre-filled conversation; otherwise the
  // remembered conversation stays open. Either way we then render the shared chat.
  useEffect(() => {
    let alive = true;
    let seed: ChatSeed | null = null;
    try {
      const raw = localStorage.getItem(CHAT_SEED_KEY);
      if (raw) {
        localStorage.removeItem(CHAT_SEED_KEY);
        const s = JSON.parse(raw) as ChatSeed;
        if (s && (s.prompt || s.answer)) seed = s;
      }
    } catch {
      // no / unreadable seed — fall through to the remembered conversation
    }
    if (seed) {
      adapter
        .consumeSeed(seed)
        .then((id) => alive && selectActive(id))
        .finally(() => alive && setReady(true));
    } else {
      setReady(true);
    }
    return () => {
      alive = false;
    };
  }, [adapter, selectActive]);

  const addParts = useCallback(
    (parts: InlinePart[]) => {
      if (!parts.length) return;
      setAttached((cur) => {
        const { next, dropped } = mergeAttach(cur, parts);
        if (dropped > 0)
          ui.toast({ title: `${dropped} file(s) not attached`, description: 'Too many or too large (max 25 files, 24 MB).', variant: 'error' });
        return next;
      });
    },
    [ui],
  );

  const addFiles = useCallback(
    async (files: File[]) => {
      if (!files.length) return;
      const parts = (await Promise.all(files.map(fileToInline))).filter(Boolean) as InlinePart[];
      addParts(parts);
    },
    [addParts],
  );

  function onDrop(e: DragEvent<HTMLDivElement>) {
    e.preventDefault();
    setDragOver(false);
    const files = Array.from(e.dataTransfer?.files ?? []);
    if (files.length) void addFiles(files);
  }

  const effortLabel = EFFORTS.find((e) => e.value === effort)?.label ?? 'Auto';
  const effortItems: MenuItem[] = EFFORTS.map((e) => ({
    id: e.value || 'auto',
    label: e.label,
    checked: e.value === effort,
    onSelect: () => setEffort(e.value),
  }));

  // The composer accessory carries aigentic's OWN composer extras (kept out of the shared chat):
  // attach-from-desktop, attach-from-Files, the Claude effort level, and the current attachments.
  const composerAccessory = (
    <Stack gap={1} className="min-w-0">
      {attached.length > 0 && (
        <Stack direction="row" gap={1} align="center" className="flex-wrap">
          {attached.map((p, i) => (
            <Stack key={i} direction="row" align="center" gap={1} className="rounded-full bg-fill/10 pl-2.5 pr-1 py-0.5">
              <Text variant="caption" truncate className="max-w-[10rem]">
                {baseName(p.path)}
              </Text>
              <Button variant="ghost" size="sm" aria-label="Remove attachment" onClick={() => setAttached((a) => a.filter((_, j) => j !== i))}>
                ×
              </Button>
            </Stack>
          ))}
        </Stack>
      )}
      <Stack direction="row" gap={1} align="center">
        <UploadControl onFiles={(f) => void addFiles(f)} label="Attach" />
        <IconButton label="Attach from Files" size="sm" variant="ghost" onClick={() => setPickerOpen(true)}>
          <FilesIcon className="h-4 w-4" />
        </IconButton>
        <DropdownMenu
          align="start"
          trigger={
            <Button variant="ghost" size="sm">
              Effort: {effortLabel}
            </Button>
          }
          items={effortItems}
        />
        {effort && <Badge variant="neutral">Claude only</Badge>}
      </Stack>
    </Stack>
  );

  if (!ready) {
    return (
      <Stack align="center" justify="center" className="min-h-[40vh]">
        <Spinner className="h-6 w-6" />
      </Stack>
    );
  }

  return (
    <Stack
      className={`h-[72vh] min-h-[420px] rounded-md ${dragOver ? 'ring-2 ring-accent ring-offset-2 ring-offset-transparent' : ''}`}
      onDragOver={(e) => {
        e.preventDefault();
        if (!dragOver) setDragOver(true);
      }}
      onDragLeave={(e) => {
        if (e.currentTarget === e.target) setDragOver(false);
      }}
      onDrop={onDrop}
    >
      <Chat
        adapter={adapter}
        activeId={activeId}
        onActiveChange={selectActive}
        composerAccessory={composerAccessory}
        placeholder="Message the AI…  (Enter to send, Shift+Enter for a new line — drop files to attach)"
      />
      {pickerOpen && <FilesPicker api={apiFor('samba')} onClose={() => setPickerOpen(false)} onPick={addParts} />}
    </Stack>
  );
}
