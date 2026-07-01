import { useState } from 'react';
import {
  Badge,
  Button,
  CodeBlock,
  Panel,
  PasswordInput,
  Stack,
  Text,
  useLiveQuery,
  type ServiceContextProps,
} from '@holistic/ui';
import type { SecretStatus, TokenStatus } from './types';

// ConnectAiPanel lets a user link THEIR OWN AI credentials so no admin bears the token load:
//   - an Anthropic API key  → bills the paid claude-api engine to their own Console account;
//   - a Claude subscription token (from `claude setup-token`) → uses their Pro/Max plan for the
//     claude-cli engine at no API cost.
// Both are write-only from the UI: the backend returns only a masked status, never the secret.
export function ConnectAiPanel({ api, ui }: Pick<ServiceContextProps, 'api' | 'ui'>) {
  return (
    <Stack gap={4}>
      <ApiKeySlot api={api} ui={ui} />
      <ClaudeSlot api={api} ui={ui} />
    </Stack>
  );
}

function ApiKeySlot({ api, ui }: Pick<ServiceContextProps, 'api' | 'ui'>) {
  const status = useLiveQuery<SecretStatus>(() => api.get<SecretStatus>('mykey'), 30000);
  const [key, setKey] = useState('');
  const [busy, setBusy] = useState(false);
  const cur = status.data;
  const ownKey = cur?.source === 'user';

  async function save() {
    const t = key.trim();
    if (!t) return;
    setBusy(true);
    try {
      await api.post<SecretStatus>('mykey', { key: t });
      setKey('');
      ui.toast({ title: 'Your API key was saved', variant: 'success' });
      status.refresh();
    } catch (e) {
      ui.toast({ title: 'Could not save key', description: (e as Error).message, variant: 'error' });
    } finally {
      setBusy(false);
    }
  }
  async function clear() {
    setBusy(true);
    try {
      await api.post<SecretStatus>('mykey', { clear: true });
      ui.toast({ title: 'Your API key was removed', variant: 'success' });
      status.refresh();
    } catch (e) {
      ui.toast({ title: 'Could not remove key', description: (e as Error).message, variant: 'error' });
    } finally {
      setBusy(false);
    }
  }

  return (
    <Panel title="Your Anthropic API key" className="p-4">
      <Stack gap={3}>
        <Stack direction="row" align="center" gap={2}>
          {cur?.configured ? (
            <>
              <Badge variant={ownKey ? 'accent' : 'neutral'}>{ownKey ? 'your key' : 'using shared key'}</Badge>
              {cur.hint && (
                <Text variant="footnote" color="secondary">
                  {cur.hint}
                </Text>
              )}
            </>
          ) : (
            <Badge variant="neutral">not configured</Badge>
          )}
        </Stack>
        <Text color="secondary">
          Bills the paid claude-api engine to your own Anthropic Console account. Create one at
          console.anthropic.com → API Keys. Stored server-side, never shown again.
        </Text>
        <PasswordInput value={key} onChange={(e) => setKey(e.target.value)} placeholder="sk-ant-…" />
        <Stack direction="row" gap={2}>
          <Button variant="primary" loading={busy} disabled={!key.trim()} onClick={save}>
            {ownKey ? 'Replace key' : 'Save key'}
          </Button>
          {ownKey && (
            <Button variant="secondary" loading={busy} onClick={clear}>
              Remove
            </Button>
          )}
        </Stack>
      </Stack>
    </Panel>
  );
}

function ClaudeSlot({ api, ui }: Pick<ServiceContextProps, 'api' | 'ui'>) {
  const status = useLiveQuery<TokenStatus>(() => api.get<TokenStatus>('claude'), 30000);
  const [token, setToken] = useState('');
  const [busy, setBusy] = useState(false);
  const cur = status.data;

  async function link() {
    const t = token.trim();
    if (!t) return;
    setBusy(true);
    try {
      await api.post<TokenStatus>('claude/link', { token: t });
      setToken('');
      ui.toast({ title: 'Claude subscription linked', variant: 'success' });
      status.refresh();
    } catch (e) {
      ui.toast({ title: 'Could not link Claude', description: (e as Error).message, variant: 'error' });
    } finally {
      setBusy(false);
    }
  }
  async function unlink() {
    setBusy(true);
    try {
      await api.post<TokenStatus>('claude/unlink', {});
      ui.toast({ title: 'Claude subscription unlinked', variant: 'success' });
      status.refresh();
    } catch (e) {
      ui.toast({ title: 'Could not unlink', description: (e as Error).message, variant: 'error' });
    } finally {
      setBusy(false);
    }
  }

  return (
    <Panel title="Your Claude subscription" className="p-4">
      <Stack gap={3}>
        <Stack direction="row" align="center" gap={2}>
          {cur?.linked ? (
            <>
              <Badge variant="accent">linked</Badge>
              {cur.hint && (
                <Text variant="footnote" color="secondary">
                  {cur.hint}
                </Text>
              )}
            </>
          ) : (
            <Badge variant="neutral">not linked</Badge>
          )}
        </Stack>
        <Text color="secondary">
          Uses your Claude Pro/Max subscription for the claude-cli engine — no API cost. One-time
          setup: on a computer where the <Text as="span" className="font-mono">claude</Text> CLI is
          installed and you can sign into your Claude account (your laptop/desktop — not this
          server), run the command below, sign in, then paste the <Text as="span" className="font-mono">sk-ant-oat…</Text>{' '}
          token it prints (valid about a year).
        </Text>
        <CodeBlock code="claude setup-token" />
        <PasswordInput value={token} onChange={(e) => setToken(e.target.value)} placeholder="sk-ant-oat…" />
        <Stack direction="row" gap={2}>
          <Button variant="primary" loading={busy} disabled={!token.trim()} onClick={link}>
            {cur?.linked ? 'Replace token' : 'Link Claude'}
          </Button>
          {cur?.linked && (
            <Button variant="secondary" loading={busy} onClick={unlink}>
              Unlink
            </Button>
          )}
        </Stack>
      </Stack>
    </Panel>
  );
}
