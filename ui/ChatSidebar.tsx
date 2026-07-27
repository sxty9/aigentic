import { Button, IconButton, PlusIcon, ScrollArea, SearchField, Stack, Text, TrashIcon, useT } from '@holistic/ui';
import { NEW_CHAT_TITLE, type ChatStore } from './chatStore';

// ChatSidebar is the Perplexity-style chat list: a "New chat" button, a search box that filters
// by title or message content, and the chats themselves (newest first). It is purely a view over
// the chat store. The placeholder "New chat" title is stored as a stable sentinel and localized
// here on display, so a German user sees "Neuer Chat" without the stored data ever changing.
export function ChatSidebar({ store }: { store: ChatStore }) {
  const t = useT();
  return (
    <Stack gap={2} className="h-full">
      <Button variant="primary" size="sm" iconLeft={<PlusIcon className="h-4 w-4" />} onClick={store.newChat}>
        {t('aigentic.chat.newChat')}
      </Button>
      <SearchField value={store.search} onChange={store.setSearch} placeholder={t('aigentic.chat.searchChats')} />
      <ScrollArea className="grow max-h-[52vh] -mr-1 pr-1">
        {store.filtered.length === 0 ? (
          <Text variant="caption" color="tertiary">
            {store.search ? t('aigentic.chat.noMatches') : t('aigentic.chat.noChats')}
          </Text>
        ) : (
          <Stack gap={1}>
            {store.filtered.map((c) => (
              <Stack key={c.id} direction="row" align="center" gap={1}>
                <Button
                  variant={c.id === store.activeId ? 'secondary' : 'ghost'}
                  size="sm"
                  className="grow min-w-0 justify-start"
                  onClick={() => store.selectChat(c.id)}
                >
                  <Text truncate className="w-full text-left">
                    {c.title === NEW_CHAT_TITLE ? t('aigentic.chat.newChat') : c.title}
                  </Text>
                </Button>
                <IconButton label={t('aigentic.chat.deleteChat')} size="sm" variant="ghost" onClick={() => store.deleteChat(c.id)}>
                  <TrashIcon className="h-4 w-4" />
                </IconButton>
              </Stack>
            ))}
          </Stack>
        )}
      </ScrollArea>
    </Stack>
  );
}
