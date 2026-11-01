# Support chat

## ADDED Requirements

### Requirement: Opt-in chat behind the customer-support switch

Chat MUST be available only when both `support_ticket_enabled` and `support_chat_enabled` are true. `support_chat_enabled` MUST default to false. Every chat endpoint MUST fail closed when the settings cannot be read. Ticket endpoints MUST keep their existing gate.

#### Scenario: Upgrade with tickets already on

- **WHEN** a site that enabled tickets upgrades
- **THEN** no bubble or chat appears until an administrator turns on 在线客服, and tickets behave as before.

### Requirement: Ordering-safe message delivery

Every transaction that inserts a chat message MUST lock the conversation row before the message id is drawn, so message ids follow commit order. Clients MUST advance their poll cursor only from `after_id` responses; responses to sends MUST NOT move the cursor.

#### Scenario: Concurrent staff and user messages

- **WHEN** a staff reply and a user message are written concurrently
- **THEN** a client polling with its previous highest id receives both messages.

### Requirement: Waiting queue

A user message MUST start the wait if none is running and MUST NOT reset a running wait. A staff message or resolving the conversation MUST clear it. System messages MUST NOT change it and bot messages MUST NOT clear it. Resolving MUST be rejected with 409 when a user message newer than the resolver's last seen id exists.

#### Scenario: Follow-up while waiting

- **WHEN** a waiting user sends 「还在吗？」
- **THEN** the conversation keeps its original wait start and position in the queue.

### Requirement: Idempotent sends and monotonic read cursors

A repeated `client_msg_id` in the same conversation MUST return the stored message with `duplicate: true` without consuming rate limits or storing another image. Read cursors MUST only move forward and MUST NOT pass the last message id. Reads MUST NOT mark messages as read.

#### Scenario: Resend after a lost response

- **WHEN** the client resends a message whose first attempt was stored
- **THEN** exactly one message exists and the client shows it as delivered.

#### Scenario: Staff see which replies were read

- **WHEN** staff send two replies and the user then opens the conversation
- **THEN** both replies show 「未读」 in the admin inbox until the user's read cursor passes them, then 「已读」 after the next poll.

### Requirement: Image messages

The server MUST accept only JPEG, PNG and WebP detected by content, at most 1.5 MiB, 4096 pixels per side and 16,777,216 pixels, and MUST derive the stored content type itself. Images MUST be readable only by the conversation owner and by staff with view permission, with `nosniff` and a sandbox CSP. Purged or deleted images MUST return 410. The retention job MUST purge images older than the configured days even while the switch is off and MUST skip a cycle when it cannot read the configuration.

#### Scenario: Oversized dimensions

- **WHEN** an upload declares 16000×16000 pixels in under 1.5 MiB
- **THEN** the server rejects it with `SUPPORT_CHAT_IMAGE_TOO_LARGE`.

### Requirement: Staff moderation and privacy

Staff with manage permission MUST be able to delete a single message (body and image removed, row kept), delete a whole conversation (a block on that user survives the deletion), and block or unblock a user from chat. Operators MUST NOT receive user balance or registration time. Message bodies MUST NOT be written to the audit log.

#### Scenario: Leaked API key

- **WHEN** staff delete a message containing a pasted key
- **THEN** both sides show 「该消息已删除」 after their next poll and the text is no longer returned by any endpoint.

#### Scenario: Deleting a blocked user's conversation

- **WHEN** staff block a user and then delete that user's conversation
- **THEN** all messages and images are removed, the user's client clears the old thread, and the user's next message is still rejected as blocked.

### Requirement: Two-way recall and staff identity

Users MUST be able to recall their own message within 2 minutes of sending it, judged by the database clock; later attempts MUST fail with 409 `SUPPORT_CHAT_RECALL_EXPIRED` and other people's messages MUST look not found. Staff with manage permission MUST be able to recall staff messages at any time. A recall MUST clear the text and image like a delete and mark the message `recalled`. User-facing responses MUST name staff only by account id (`staff_id`), MUST NOT carry staff names or e-mail addresses, and MUST NOT reveal how far staff have read.

#### Scenario: User takes back a pasted key

- **WHEN** a user recalls a message one minute after sending it
- **THEN** both sides show a recall notice, the text and image are gone, and a wait started only by that message ends.

#### Scenario: Late recall

- **WHEN** a user tries to recall a message three minutes after sending it
- **THEN** the server answers 409 `SUPPORT_CHAT_RECALL_EXPIRED` and the message stays.

#### Scenario: Staff identity

- **WHEN** staff account 7 answers a chat or a ticket
- **THEN** the user sees 「客服 #7」 and no response to the user contains that account's username or e-mail.

### Requirement: Quiet alerts and audited reads

Staff alerts MUST poll only a counts-only summary that is not audited. Conversation lists, details, message reads and image reads MUST be audited, except incremental polls that return no messages and read-cursor reports.

#### Scenario: Idle admin tab

- **WHEN** an admin tab stays open for a day with no new messages
- **THEN** no audit rows are written by the alert polling.
