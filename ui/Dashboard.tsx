import { useState } from 'react';
import {
  Badge,
  Button,
  ContentRegion,
  Panel,
  PasswordInput,
  SegmentedControl,
  Stack,
  Text,
  useLiveQuery,
  useT,
  type ServiceContextProps,
} from '@holistic/ui';
import type { Info, SecretStatus } from './types';
import { ConnectAiPanel } from './ConnectAiPanel';
import { ChatTab } from './ChatTab';

// The aigentic tab is the user's AI surface: a full chat (pick a model or let Auto choose),
// plus per-user self-service to link THEIR OWN Claude (no admin bears the token load). Admins
// additionally see the shared fallback-key panel. The same AI is also reachable folder-scoped
// from the Files app's "Ask AI" action, which can hand a conversation off into this chat.
export function Dashboard({ user, api, apiFor, ui }: ServiceContextProps) {
  const [view, setView] = useState<'chat' | 'connect'>('chat');
  const t = useT();

  return (
    <ContentRegion>
      <Stack gap={4}>
        <Stack direction="row" align="center" justify="between" className="flex-wrap" gap={2}>
          <Stack direction="row" align="center" gap={2}>
            <Text variant="subhead" weight="semibold">
              Aigentic
            </Text>
            {user.isAdmin && <Badge variant="accent">{t('aigentic.admin')}</Badge>}
          </Stack>
          <SegmentedControl
            value={view}
            onChange={setView}
            options={[
              { value: 'chat', label: t('aigentic.tab.chat') },
              { value: 'connect', label: t('aigentic.tab.connect') },
            ]}
          />
        </Stack>

        {view === 'chat' ? (
          <ChatTab api={api} apiFor={apiFor} ui={ui} />
        ) : (
          <ConnectView api={api} ui={ui} isAdmin={user.isAdmin} />
        )}
      </Stack>
    </ContentRegion>
  );
}

// ConnectView gathers the credential panels: the user links their own Claude (and Anthropic
// key); admins additionally manage the optional shared fallback key.
function ConnectView({ api, ui, isAdmin }: Pick<ServiceContextProps, 'api' | 'ui'> & { isAdmin: boolean }) {
  const info = useLiveQuery<Info>(() => api.get<Info>('info'), 10000);
  const t = useT();
  return (
    <Stack gap={4}>
      <Panel title={t('aigentic.connect.serviceTitle')} className="p-4">
        {info.data ? (
          <Stack gap={2}>
            <Stack direction="row" align="center" gap={2}>
              <Text weight="semibold">{info.data.service}</Text>
              <Badge variant="neutral">v{info.data.version}</Badge>
            </Stack>
            <Text color="secondary">
              {t('aigentic.connect.serviceIntro', { kinds: info.data.kinds.join(', ') || '—' })}
            </Text>
          </Stack>
        ) : (
          <Text color={info.loading ? 'secondary' : 'danger'}>
            {info.loading ? t('aigentic.loading') : t('aigentic.connect.serviceLoadError')}
          </Text>
        )}
      </Panel>

      <ConnectAiPanel api={api} ui={ui} />

      {isAdmin && <GlobalKeyPanel api={api} ui={ui} />}
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
  const t = useT();
  const current = status.data;

  async function save() {
    const trimmed = key.trim();
    if (!trimmed) return;
    setBusy(true);
    try {
      await api.post<SecretStatus>('secret', { key: trimmed });
      setKey('');
      ui.toast({ title: t('aigentic.key.sharedSaved'), variant: 'success' });
      status.refresh();
    } catch (e) {
      ui.toast({ title: t('aigentic.key.saveError'), description: (e as Error).message, variant: 'error' });
    } finally {
      setBusy(false);
    }
  }

  async function remove() {
    setBusy(true);
    try {
      await api.post<SecretStatus>('secret', { clear: true });
      ui.toast({ title: t('aigentic.key.sharedRemoved'), variant: 'success' });
      status.refresh();
    } catch (e) {
      ui.toast({ title: t('aigentic.key.removeError'), description: (e as Error).message, variant: 'error' });
    } finally {
      setBusy(false);
    }
  }

  return (
    <Panel title={t('aigentic.key.sharedTitle')} className="p-4">
      <Stack gap={3}>
        <Stack direction="row" align="center" gap={2}>
          {current?.configured ? (
            <>
              <Badge variant="accent">{t('aigentic.key.configured')}</Badge>
              {current.hint && <Text color="secondary">{current.hint}</Text>}
              {current.source && <Badge variant="neutral">{current.source}</Badge>}
            </>
          ) : (
            <Badge variant="neutral">{t('aigentic.key.notConfigured')}</Badge>
          )}
        </Stack>
        <Text color="secondary">{t('aigentic.key.sharedIntro')}</Text>
        <PasswordInput
          value={key}
          onChange={(e) => setKey(e.target.value)}
          placeholder={current?.configured ? t('aigentic.key.replacePlaceholder') : t('aigentic.key.placeholder')}
        />
        <Stack direction="row" gap={2}>
          <Button variant="primary" loading={busy} disabled={!key.trim()} onClick={save}>
            {current?.configured ? t('aigentic.key.replace') : t('aigentic.key.save')}
          </Button>
          {current?.configured && current.source === 'store' && (
            <Button variant="secondary" loading={busy} onClick={remove}>
              {t('aigentic.remove')}
            </Button>
          )}
        </Stack>
      </Stack>
    </Panel>
  );
}
