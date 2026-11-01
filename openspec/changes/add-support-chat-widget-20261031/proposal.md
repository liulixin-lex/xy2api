## Why

0.4.14 added website support tickets (`/support-tickets`). Tickets are asynchronous, need a title and category, refresh every 30 seconds and carry text only. Users of an API relay mostly need quick answers and want to send an error screenshot. The operator asked for a site-wide floating support bubble that opens a direct chat with staff and can also create tickets, with tickets folded into one "客户服务" (customer support) entry.

## What Changes

- Add an online support chat: one long-lived conversation per user with staff, text plus images, near-real-time delivery by adaptive polling.
- Add a floating, draggable, edge-snapping circular bubble (default bottom-right) mounted once in the app shell. Guests see it and are asked to log in; administrators and operators do not see it.
- Turn the user `/support-tickets` page and the admin `/admin/support-tickets` page into "客户服务" hubs with chat and ticket tabs; rename the sidebar entry and the settings card from 在线工单／网站工单 to 客户服务.
- Add an admin inbox (waiting queue ordered by wait start, reply with images, resolve, delete message, delete conversation, block from chat) and in-admin alerts (badge, `(N)` title prefix, optional sound and desktop notification) driven by a counts-only summary.
- Add images: client-side compression on input, server-side format and size checks, storage in PostgreSQL, authenticated reads, retention purge.
- Add a `support_chat_enabled` sub-switch (default off) under the existing `support_ticket_enabled` switch, plus `chat_welcome` and `chat_image_retention_days` in the ticket config.

## Capabilities

### New Capabilities

- `support-chat`: per-user support conversation with ordering-safe polling, read cursors, waiting queue, image messages, moderation, alerts and the floating widget.

### Modified Capabilities

- `website-support-tickets` (from `add-support-tickets-and-managed-openai-totp-20261029`): user-facing name becomes 客户服务; ticket list pages become a tab of the hub. Ticket tables, routes, limits, permissions and audit stay unchanged.

## Impact

- Database: append-only migration 287 (`support_chats`, `support_chat_messages`, `support_chat_images`) and its checksum.
- Backend: new chat service, repository, handler, image validation and retention job; settings, public settings, routes, Wire, operator permission catalog and audit entries.
- Frontend: global widget, chat components, image compression, chat store, staff alert composable, hub pages, admin inbox, settings card and zh/en copy.
- Operations: images are stored in the database and therefore included in database backups until purged; `chat_image_retention_days` is the size lever.

## Compatibility

- `support_chat_enabled` defaults off, so sites that already enabled tickets keep tickets-only behaviour after upgrade; turning chat on is an explicit admin action.
- The permission module key `support_tickets`, the switch `support_ticket_enabled`, all ticket routes and the published migrations are unchanged.
- Rollback: migration 287 only adds tables. An older binary ignores them; the data stays until the tables are dropped manually.
