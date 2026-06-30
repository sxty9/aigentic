import { BoltIcon, registerFolderAction, userHasRight, type ServicePlugin } from '@holistic/ui';
import { Dashboard } from './Dashboard';
import { AskAiPanel } from './AskAiPanel';

// The end-user AI feature is delivered ENTIRELY by aigentic, contributed into the shared Files
// toolbar as a folder-level action (no Samba code knows about it). Visible to anyone holding
// the run right (admins always); the paid path is enforced server-side.
registerFolderAction({
  id: 'aigentic.ask',
  label: 'Ask AI',
  icon: BoltIcon,
  visible: (user) => userHasRight(user, 'hp_aigentic_run'),
  Panel: AskAiPanel,
});

// The aigentic tab is PER-USER self-service: anyone with the run right opens it to link their
// own Claude (API key + subscription token); admins also see the shared fallback-key panel.
// End-user AI usage lives in the Files app's "Ask AI" action. `id` MUST equal the link dir name
// and the permissions manifest's "service" field.
const plugin: ServicePlugin = {
  id: 'aigentic',
  displayName: 'Aigentic',
  icon: BoltIcon,
  order: 100,
  visible: (user) => userHasRight(user, 'hp_aigentic_run'),
  Component: Dashboard,
};

export default plugin;
