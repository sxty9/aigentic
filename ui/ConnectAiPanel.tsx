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
  useT,
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
  const t = useT();
  const cur = status.data;
  const ownKey = cur?.source === 'user';

  async function save() {
    const trimmed = key.trim();
    if (!trimmed) return;
    setBusy(true);
    try {
      await api.post<SecretStatus>('mykey', { key: trimmed });
      setKey('');
      ui.toast({ title: t('aigentic.mykey.saved'), variant: 'success' });
      status.refresh();
    } catch (e) {
      ui.toast({ title: t('aigentic.key.saveError'), description: (e as Error).message, variant: 'error' });
    } finally {
      setBusy(false);
    }
  }
  async function clear() {
    setBusy(true);
    try {
      await api.post<SecretStatus>('mykey', { clear: true });
      ui.toast({ title: t('aigentic.mykey.removed'), variant: 'success' });
      status.refresh();
    } catch (e) {
      ui.toast({ title: t('aigentic.key.removeError'), description: (e as Error).message, variant: 'error' });
    } finally {
      setBusy(false);
    }
  }

  return (
    <Panel title={t('aigentic.mykey.title')} className="p-4">
      <Stack gap={3}>
        <Stack direction="row" align="center" gap={2}>
          {cur?.configured ? (
            <>
              <Badge variant={ownKey ? 'accent' : 'neutral'}>{ownKey ? t('aigentic.mykey.own') : t('aigentic.mykey.shared')}</Badge>
              {cur.hint && (
                <Text variant="footnote" color="secondary">
                  {cur.hint}
                </Text>
              )}
            </>
          ) : (
            <Badge variant="neutral">{t('aigentic.key.notConfigured')}</Badge>
          )}
        </Stack>
        <Text color="secondary">{t('aigentic.mykey.intro')}</Text>
        <PasswordInput value={key} onChange={(e) => setKey(e.target.value)} placeholder={t('aigentic.key.placeholder')} />
        <Stack direction="row" gap={2}>
          <Button variant="primary" loading={busy} disabled={!key.trim()} onClick={save}>
            {ownKey ? t('aigentic.key.replace') : t('aigentic.key.save')}
          </Button>
          {ownKey && (
            <Button variant="secondary" loading={busy} onClick={clear}>
              {t('aigentic.remove')}
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
  const t = useT();
  const cur = status.data;

  async function link() {
    const trimmed = token.trim();
    if (!trimmed) return;
    setBusy(true);
    try {
      await api.post<TokenStatus>('claude/link', { token: trimmed });
      setToken('');
      ui.toast({ title: t('aigentic.claude.linkedToast'), variant: 'success' });
      status.refresh();
    } catch (e) {
      ui.toast({ title: t('aigentic.claude.linkError'), description: (e as Error).message, variant: 'error' });
    } finally {
      setBusy(false);
    }
  }
  async function unlink() {
    setBusy(true);
    try {
      await api.post<TokenStatus>('claude/unlink', {});
      ui.toast({ title: t('aigentic.claude.unlinkedToast'), variant: 'success' });
      status.refresh();
    } catch (e) {
      ui.toast({ title: t('aigentic.claude.unlinkError'), description: (e as Error).message, variant: 'error' });
    } finally {
      setBusy(false);
    }
  }

  return (
    <Panel title={t('aigentic.claude.title')} className="p-4">
      <Stack gap={3}>
        <Stack direction="row" align="center" gap={2}>
          {cur?.linked ? (
            <>
              <Badge variant="accent">{t('aigentic.claude.linked')}</Badge>
              {cur.hint && (
                <Text variant="footnote" color="secondary">
                  {cur.hint}
                </Text>
              )}
            </>
          ) : (
            <Badge variant="neutral">{t('aigentic.claude.notLinked')}</Badge>
          )}
        </Stack>
        <Text color="secondary">{t('aigentic.claude.intro')}</Text>
        <CodeBlock code="claude setup-token" />
        <PasswordInput value={token} onChange={(e) => setToken(e.target.value)} placeholder={t('aigentic.claude.tokenPlaceholder')} />
        <Stack direction="row" gap={2}>
          <Button variant="primary" loading={busy} disabled={!token.trim()} onClick={link}>
            {cur?.linked ? t('aigentic.claude.replaceToken') : t('aigentic.claude.link')}
          </Button>
          {cur?.linked && (
            <Button variant="secondary" loading={busy} onClick={unlink}>
              {t('aigentic.claude.unlink')}
            </Button>
          )}
        </Stack>
      </Stack>
    </Panel>
  );
}
