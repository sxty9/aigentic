import { BoltIcon, registerFolderAction, registerViewerAction, userHasRight, type ServicePlugin } from '@holistic/ui';
import { Dashboard } from './Dashboard';
import { AskAiFolderPanel, AskAiFilePanel } from './aiExchange';
import { aiReadable } from './aiFiles';

// The end-user AI feature is delivered ENTIRELY by aigentic, contributed into two shared surfaces
// (no Files/Mail code knows about it). Visible to anyone holding the run right (admins always);
// the paid path is enforced server-side.
//
// 1) Folder-level action on the Files toolbar — asks about a folder / multi-file selection.
registerFolderAction({
  id: 'aigentic.ask',
  label: 'Ask AI',
  icon: BoltIcon,
  visible: (user) => userHasRight(user, 'hp_aigentic_run'),
  Panel: AskAiFolderPanel,
});

// 2) File-level action inside the shared FilePreview — asks about the ONE displayed file, wherever
// it is previewed (Files, Mail attachments, …). Gated to types the AI can actually read; the host
// supplies the file content, so this needs no fileshare access of its own.
registerViewerAction({
  id: 'aigentic.ask.file',
  label: 'Ask AI',
  icon: BoltIcon,
  visible: (user) => userHasRight(user, 'hp_aigentic_run'),
  applies: aiReadable,
  Panel: AskAiFilePanel,
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
