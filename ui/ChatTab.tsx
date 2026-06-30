import { Stack, type ServiceContextProps } from '@holistic/ui';
import { usePicker } from './EnginePicker';
import { useChats } from './chatStore';
import { ChatSidebar } from './ChatSidebar';
import { ChatView } from './ChatView';

// ChatTab is the aigentic tab's full chat surface: a persistent, searchable history sidebar on
// the left (Perplexity-style) and the active conversation on the right. The chat store owns the
// list + persistence; the picker is owned here so the engine choice survives switching chats.
export function ChatTab({ api, apiFor, ui }: Pick<ServiceContextProps, 'api' | 'apiFor' | 'ui'>) {
  const store = useChats();
  const picker = usePicker(apiFor);

  return (
    <Stack direction="row" gap={4} align="stretch" className="min-h-[62vh]">
      <Stack className="w-60 shrink-0">
        <ChatSidebar store={store} />
      </Stack>
      <Stack grow className="min-w-0">
        <ChatView
          key={store.activeId}
          api={api}
          ui={ui}
          picker={picker}
          messages={store.active?.messages ?? []}
          onMessages={store.setActiveMessages}
        />
      </Stack>
    </Stack>
  );
}
