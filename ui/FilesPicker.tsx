import { useEffect, useMemo, useState } from 'react';
import {
  Box,
  Breadcrumb,
  Button,
  FileBrowser,
  Modal,
  SegmentedControl,
  Spinner,
  Stack,
  useT,
  type BreadcrumbSegment,
  type FileEntry,
  type ServiceApiClient,
} from '@holistic/ui';
import { encodePath, readEntryInline, type InlinePart } from './aiFiles';

// A picker over the user's Holistic Files (Samba) share, mounted in the chat so files can be attached
// from the server-side share — the counterpart to dragging in local OS files. It reuses the SAME fs
// endpoints as the Files app (fs/roots, fs/list) and the SAME shared reader (readEntryInline over
// fs/text + fs/raw) and SDK browser, so there is no parallel data path: the daemon stays fs-free, the
// privileged Samba client hands over the bytes.
const MAX_FILES = 25;

type FileRoot = { key: string; label?: string; writable?: boolean };

// segsFor turns a virtual path ("me/Docs/spec.md") into breadcrumb hops.
function segsFor(path: string): BreadcrumbSegment[] {
  let acc = '';
  return path.split('/').map((name) => {
    acc = acc ? `${acc}/${name}` : name;
    return { label: name, path: acc };
  });
}

export function FilesPicker({ api, onClose, onPick }: { api: ServiceApiClient; onClose: () => void; onPick: (parts: InlinePart[]) => void }) {
  const t = useT();
  const [roots, setRoots] = useState<FileRoot[]>([]);
  const [cwd, setCwd] = useState('');
  const [entries, setEntries] = useState<FileEntry[]>([]);
  const [selection, setSelection] = useState<Set<string>>(new Set());
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  // Roots → start at the first share (usually "me"), matching the Files app.
  useEffect(() => {
    let alive = true;
    api
      .get<FileRoot[]>('fs/roots')
      .then((rs) => {
        if (!alive) return;
        setRoots(rs);
        setCwd((cur) => cur || rs[0]?.key || '');
      })
      .catch((e) => alive && setError((e as Error).message));
    return () => {
      alive = false;
    };
  }, [api]);

  // List the current folder.
  useEffect(() => {
    if (!cwd) return;
    let alive = true;
    setLoading(true);
    setError(null);
    setSelection(new Set());
    api
      .get<{ entries: FileEntry[] }>(`fs/list?path=${encodePath(cwd)}`)
      .then((r) => alive && setEntries(r.entries ?? []))
      .catch((e) => alive && setError((e as Error).message))
      .finally(() => alive && setLoading(false));
    return () => {
      alive = false;
    };
  }, [api, cwd]);

  const rootKey = useMemo(() => cwd.split('/')[0], [cwd]);

  // Open a dir → navigate into it; open a file → toggle it in the selection.
  function onOpen(e: FileEntry) {
    if (e.kind === 'dir') {
      setCwd(e.path);
      return;
    }
    setSelection((s) => {
      const next = new Set(s);
      if (next.has(e.path)) next.delete(e.path);
      else next.add(e.path);
      return next;
    });
  }

  async function attach() {
    const files = entries.filter((e) => e.kind === 'file' && selection.has(e.path)).slice(0, MAX_FILES);
    if (files.length === 0) return;
    setBusy(true);
    try {
      const parts = (await Promise.all(files.map((e) => readEntryInline(api, e)))).filter(Boolean) as InlinePart[];
      onPick(parts);
      onClose();
    } finally {
      setBusy(false);
    }
  }

  const count = entries.filter((e) => e.kind === 'file' && selection.has(e.path)).length;

  return (
    <Modal
      open
      onOpenChange={(o) => !o && onClose()}
      title={t('aigentic.filesPicker.title')}
      size="lg"
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            {t('common.cancel')}
          </Button>
          <Button variant="primary" onClick={attach} disabled={count === 0 || busy} loading={busy}>
            {count > 0 ? t('aigentic.attachCount', { count }) : t('aigentic.attach')}
          </Button>
        </>
      }
    >
      <Stack gap={2}>
        {roots.length > 1 && (
          <SegmentedControl
            value={rootKey}
            onChange={(k) => setCwd(k)}
            options={roots.map((r) => ({ value: r.key, label: r.label ?? r.key }))}
          />
        )}
        <Breadcrumb segments={segsFor(cwd)} onNavigate={(p) => setCwd(p)} />
        <Box className="max-h-[46vh] min-h-[20vh] overflow-auto">
          {loading ? (
            <Stack align="center" justify="center" className="min-h-[20vh]">
              <Spinner className="h-5 w-5" />
            </Stack>
          ) : (
            <FileBrowser
              entries={entries}
              view="list"
              selection={selection}
              error={error}
              onOpen={onOpen}
              onSelectionChange={setSelection}
              onAction={() => {}}
            />
          )}
        </Box>
      </Stack>
    </Modal>
  );
}
