import { useState } from 'react';
import {
  Badge,
  Button,
  Panel,
  PasswordInput,
  Stack,
  Text,
  useLiveQuery,
  type ServiceContextProps,
} from '@holistic/ui';
import type { Info, SecretStatus } from './types';

// The aigentic tab is ADMIN-ONLY configuration (the plugin's `visible` gate restricts it to
// admins). End-user AI usage lives in the Files app's folder-level "Ask AI" action, not here.
export function Dashboard({ api, ui }: ServiceContextProps) {
  const info = useLiveQuery<Info>(() => api.get<Info>('info'), 10000);

  return (
    <Stack gap={4}>
      <Panel title="Aigentic" className="p-4">
        {info.data ? (
          <Stack gap={2}>
            <Stack direction="row" align="center" gap={2}>
              <Text weight="semibold">{info.data.service}</Text>
              <Badge variant="neutral">v{info.data.version}</Badge>
              {info.data.isAdmin && <Badge variant="accent">admin</Badge>}
            </Stack>
            <Text color="secondary">
              Admin configuration for the AI engines. Users run AI from the Files app — open a
              folder and choose “Ask AI”. Processors: {info.data.kinds.join(', ') || '—'}
            </Text>
          </Stack>
        ) : (
          <Text color={info.loading ? 'secondary' : 'danger'}>
            {info.loading ? 'Loading…' : 'Could not load service info.'}
          </Text>
        )}
      </Panel>

      <SecretPanel api={api} ui={ui} />
    </Stack>
  );
}

// SecretPanel lets an admin set, replace or remove the Anthropic API key the paid engines use.
// The key is write-only from the UI: the backend returns a masked hint, never the value.
function SecretPanel({ api, ui }: Pick<ServiceContextProps, 'api' | 'ui'>) {
  const status = useLiveQuery<SecretStatus>(() => api.get<SecretStatus>('secret'), 30000);
  const [key, setKey] = useState('');
  const [busy, setBusy] = useState(false);
  const current = status.data;

  async function save() {
    const trimmed = key.trim();
    if (!trimmed) return;
    setBusy(true);
    try {
      await api.post<SecretStatus>('secret', { key: trimmed });
      setKey('');
      ui.toast({ title: 'Claude API key saved', variant: 'success' });
      status.refresh();
    } catch (e) {
      ui.toast({ title: 'Could not save key', description: (e as Error).message, variant: 'error' });
    } finally {
      setBusy(false);
    }
  }

  async function remove() {
    setBusy(true);
    try {
      await api.post<SecretStatus>('secret', { clear: true });
      ui.toast({ title: 'Claude API key removed', variant: 'success' });
      status.refresh();
    } catch (e) {
      ui.toast({ title: 'Could not remove key', description: (e as Error).message, variant: 'error' });
    } finally {
      setBusy(false);
    }
  }

  return (
    <Panel title="Claude API key" className="p-4">
      <Stack gap={3}>
        <Stack direction="row" align="center" gap={2}>
          {current?.configured ? (
            <>
              <Badge variant="accent">configured</Badge>
              {current.hint && <Text color="secondary">{current.hint}</Text>}
              {current.source && <Badge variant="neutral">{current.source}</Badge>}
            </>
          ) : (
            <Badge variant="neutral">not configured</Badge>
          )}
        </Stack>
        <Text color="secondary">
          Used by the paid claude-api engine (and choose when it routes there). Stored
          server-side in a private file (mode 0600) — it is never shown again.
        </Text>
        <PasswordInput
          value={key}
          onChange={(e) => setKey(e.target.value)}
          placeholder={current?.configured ? 'paste a new key to replace (sk-ant-…)' : 'sk-ant-…'}
        />
        <Stack direction="row" gap={2}>
          <Button variant="primary" loading={busy} disabled={!key.trim()} onClick={save}>
            {current?.configured ? 'Replace key' : 'Save key'}
          </Button>
          {current?.configured && current.source === 'store' && (
            <Button variant="secondary" loading={busy} onClick={remove}>
              Remove
            </Button>
          )}
        </Stack>
      </Stack>
    </Panel>
  );
}
