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
import { ConnectAiPanel } from './ConnectAiPanel';

// The aigentic tab is per-user self-service: anyone with the run right links THEIR OWN AI
// credentials here (no admin bears the token load). Admins additionally see the shared
// fallback-key panel. End-user AI usage itself lives in the Files app's "Ask AI" action.
export function Dashboard({ user, api, ui }: ServiceContextProps) {
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
              Link your own Claude below. Then run AI from the Files app — open a folder and choose
              “Ask AI”. Processors: {info.data.kinds.join(', ') || '—'}
            </Text>
          </Stack>
        ) : (
          <Text color={info.loading ? 'secondary' : 'danger'}>
            {info.loading ? 'Loading…' : 'Could not load service info.'}
          </Text>
        )}
      </Panel>

      <ConnectAiPanel api={api} ui={ui} />

      {user.isAdmin && <GlobalKeyPanel api={api} ui={ui} />}
    </Stack>
  );
}

// GlobalKeyPanel (admin-only) manages the optional SHARED fallback Anthropic key — used by any
// user who hasn't linked their own. It's a convenience, not required: with no shared key,
// un-linked users simply fall back to the free local ollama. Write-only: masked status only.
function GlobalKeyPanel({ api, ui }: Pick<ServiceContextProps, 'api' | 'ui'>) {
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
      ui.toast({ title: 'Shared key saved', variant: 'success' });
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
      ui.toast({ title: 'Shared key removed', variant: 'success' });
      status.refresh();
    } catch (e) {
      ui.toast({ title: 'Could not remove key', description: (e as Error).message, variant: 'error' });
    } finally {
      setBusy(false);
    }
  }

  return (
    <Panel title="Shared fallback key (admin)" className="p-4">
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
          Optional shared Anthropic key used only by users who haven’t linked their own. Without
          it, un-linked users fall back to the free local engine. Stored server-side (0600).
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
