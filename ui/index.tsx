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

// The aigentic dashboard tab itself is ADMIN-ONLY configuration (API key, status). Regular
// users never see it — they use the "Ask AI" action in the Files app. `id` MUST equal the
// link dir name and the permissions manifest's "service" field.
const plugin: ServicePlugin = {
  id: 'aigentic',
  displayName: 'Aigentic',
  icon: BoltIcon,
  order: 100,
  visible: (user) => user.isAdmin,
  Component: Dashboard,
};

export default plugin;
