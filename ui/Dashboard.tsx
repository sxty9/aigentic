import { useState } from 'react';
import {
  Badge,
  Button,
  Input,
  Panel,
  PasswordInput,
  Stack,
  Text,
  useLiveQuery,
  userHasRight,
  type ServiceContextProps,
} from '@holistic/ui';
import type { AigenticRequest, Info, Result, RunResponse, SecretStatus } from './types';

// Backs permissions/aigentic.json → run:execute and internal/rights.GroupRun. The paid
// engines additionally need hp_aigentic_api (cost:api). Admins always pass. Keep the right
// constants in sync across the manifest, the backend and here.
const RUN_RIGHT = 'hp_aigentic_run';
const API_RIGHT = 'hp_aigentic_api';

const KINDS = ['choose', 'ollama', 'claude-cli', 'claude-api'] as const;

export function Dashboard({ user, api, ui }: ServiceContextProps) {
  const info = useLiveQuery<Info>(() => api.get<Info>('info'), 10000);
  const canRun = userHasRight(user, RUN_RIGHT);

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
              Signed in as {info.data.user}. Processors: {info.data.kinds.join(', ') || '—'}
            </Text>
          </Stack>
        ) : (
          <Text color={info.loading ? 'secondary' : 'danger'}>
            {info.loading ? 'Loading…' : 'Could not load service info.'}
          </Text>
        )}
      </Panel>

      {info.data?.isAdmin && <SecretPanel api={api} ui={ui} />}

      {canRun ? (
        <RunPanel user={user} api={api} ui={ui} />
      ) : (
        <Panel title="Run a request" className="p-4">
          <Text color="secondary">
            You need the “Run aigentic” right. An admin can grant it per user in the Rights
            (privleg) service.
          </Text>
        </Panel>
      )}
    </Stack>
  );
}

// SecretPanel lets an admin set, replace or remove the Anthropic API key the paid engines
// use. The key is write-only from the UI: the backend returns a masked hint, never the value.
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

function RunPanel({ user, api, ui }: Pick<ServiceContextProps, 'user' | 'api' | 'ui'>) {
  const [prompt, setPrompt] = useState('Summarize the attached files.');
  const [pathsText, setPathsText] = useState('');
  const [kind, setKind] = useState<string>('choose');
  const [result, setResult] = useState<Result | null>(null);
  const [busy, setBusy] = useState(false);

  const canApi = userHasRight(user, API_RIGHT);
  const paidBlocked = (kind === 'claude-api' || kind === 'choose') && !canApi;

  async function run() {
    setBusy(true);
    setResult(null);
    try {
      const paths = pathsText
        .split(/[\n,]/)
        .map((p) => p.trim())
        .filter(Boolean);
      const data: AigenticRequest = { prompt, paths: paths.length ? paths : undefined };
      // A prizm Request is Header₀ + opaque Data₀; Data is the one aigentic header.
      const res = await api.post<RunResponse>('run', { header: { kind }, data });
      setResult(res.data);
    } catch (e) {
      ui.toast({ title: 'Run failed', description: (e as Error).message, variant: 'error' });
    } finally {
      setBusy(false);
    }
  }

  return (
    <Panel title="Run a request" className="p-4">
      <Stack gap={3}>
        <Input value={prompt} onChange={(e) => setPrompt(e.target.value)} placeholder="prompt" />
        <Input
          value={pathsText}
          onChange={(e) => setPathsText(e.target.value)}
          placeholder="server-local paths (comma or newline separated, optional)"
        />
        <Stack direction="row" gap={2} align="center">
          {KINDS.map((k) => (
            <Button
              key={k}
              variant={kind === k ? 'primary' : 'secondary'}
              onClick={() => setKind(k)}
            >
              {k}
            </Button>
          ))}
        </Stack>

        {paidBlocked && (
          <Text color="danger">
            The “{kind}” engine can spend on the paid Claude API and needs the “Use the paid
            Claude API” (cost:api) right.
          </Text>
        )}

        <Stack direction="row">
          <Button variant="primary" loading={busy} disabled={paidBlocked} onClick={run}>
            Run
          </Button>
        </Stack>

        {result && (
          <Stack gap={2}>
            <Stack direction="row" align="center" gap={2}>
              <Badge variant="neutral">{result.engine ?? kind}</Badge>
              {result.model && <Text color="secondary">{result.model}</Text>}
              {result.decision && (
                <Text color="secondary">
                  chose {result.decision.picked}
                  {result.decision.complexity ? ` (${result.decision.complexity})` : ''} via{' '}
                  {result.decision.source}
                  {result.decision.fallback ? ' — fallback' : ''}
                </Text>
              )}
              {result.usage?.truncated && <Badge variant="accent">context truncated</Badge>}
            </Stack>
            <Text>{result.output}</Text>
          </Stack>
        )}
      </Stack>
    </Panel>
  );
}
