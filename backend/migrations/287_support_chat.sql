-- Support chat ("在线客服"): one long-lived conversation per user with support staff.
-- Named support_chat_* because chat, messages, session, conversation and live already name gateway features.
CREATE TABLE IF NOT EXISTS support_chats (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL UNIQUE REFERENCES users(id) ON DELETE CASCADE,
    status VARCHAR(16) NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'resolved')),
    waiting_since TIMESTAMPTZ,
    last_message_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_message_role VARCHAR(8) NOT NULL DEFAULT 'user' CHECK (last_message_role IN ('user', 'admin', 'system', 'bot')),
    last_message_id BIGINT NOT NULL DEFAULT 0,
    last_user_message_id BIGINT NOT NULL DEFAULT 0,
    last_staff_message_id BIGINT NOT NULL DEFAULT 0,
    user_read_message_id BIGINT NOT NULL DEFAULT 0,
    staff_read_message_id BIGINT NOT NULL DEFAULT 0,
    message_count INTEGER NOT NULL DEFAULT 0,
    revision BIGINT NOT NULL DEFAULT 0,
    content_version BIGINT NOT NULL DEFAULT 0,
    blocked_at TIMESTAMPTZ,
    blocked_reason VARCHAR(200) NOT NULL DEFAULT '',
    resolved_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_support_chats_waiting ON support_chats (waiting_since, id) WHERE status = 'open' AND waiting_since IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_support_chats_status_activity ON support_chats (status, last_message_at DESC);
CREATE TABLE IF NOT EXISTS support_chat_images (
    id BIGSERIAL PRIMARY KEY,
    chat_id BIGINT NOT NULL REFERENCES support_chats(id) ON DELETE CASCADE,
    author_role VARCHAR(8) NOT NULL CHECK (author_role IN ('user', 'admin')),
    content_type VARCHAR(16) NOT NULL CHECK (content_type IN ('image/webp', 'image/jpeg', 'image/png')),
    byte_size INTEGER NOT NULL CHECK (byte_size > 0 AND byte_size <= 1572864),
    width INTEGER NOT NULL CHECK (width BETWEEN 1 AND 4096),
    height INTEGER NOT NULL CHECK (height BETWEEN 1 AND 4096),
    data BYTEA,
    purged_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT support_chat_images_purge_state CHECK ((data IS NULL) = (purged_at IS NOT NULL)),
    CONSTRAINT support_chat_images_chat_image_key UNIQUE (chat_id, id)
);
CREATE INDEX IF NOT EXISTS idx_support_chat_images_chat_time ON support_chat_images (chat_id, created_at);
CREATE INDEX IF NOT EXISTS idx_support_chat_images_unpurged ON support_chat_images (created_at) WHERE purged_at IS NULL;
CREATE TABLE IF NOT EXISTS support_chat_messages (
    id BIGSERIAL PRIMARY KEY,
    chat_id BIGINT NOT NULL REFERENCES support_chats(id) ON DELETE CASCADE,
    author_role VARCHAR(8) NOT NULL CHECK (author_role IN ('user', 'admin', 'system', 'bot')),
    author_user_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
    body TEXT NOT NULL DEFAULT '' CHECK (char_length(body) <= 5000),
    image_id BIGINT UNIQUE,
    client_msg_id VARCHAR(64),
    deleted_at TIMESTAMPTZ,
    deleted_by_role VARCHAR(8) NOT NULL DEFAULT '' CHECK (deleted_by_role IN ('', 'admin')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT support_chat_messages_image_fk FOREIGN KEY (chat_id, image_id) REFERENCES support_chat_images (chat_id, id),
    CONSTRAINT support_chat_messages_content CHECK (deleted_at IS NOT NULL OR char_length(body) > 0 OR image_id IS NOT NULL)
);
CREATE INDEX IF NOT EXISTS idx_support_chat_messages_chat ON support_chat_messages (chat_id, id);
CREATE INDEX IF NOT EXISTS idx_support_chat_messages_chat_time ON support_chat_messages (chat_id, created_at);
CREATE UNIQUE INDEX IF NOT EXISTS idx_support_chat_messages_client_id ON support_chat_messages (chat_id, client_msg_id) WHERE client_msg_id IS NOT NULL;
