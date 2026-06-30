import { BoltIcon, type ServicePlugin } from '@holistic/ui';
import { Dashboard } from './Dashboard';

// The aigentic dashboard plugin. Linked into holistic/frontend/external/aigentic at install
// time and discovered by the host SPA's build-time registry. `id` MUST equal the link dir
// name and the permissions manifest's "service" field.
const plugin: ServicePlugin = {
  id: 'aigentic',
  displayName: 'Aigentic',
  icon: BoltIcon,
  order: 100,
  Component: Dashboard,
};

export default plugin;
