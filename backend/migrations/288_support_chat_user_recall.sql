-- Users can recall their own support chat messages: deleted_by_role also records 'user'.
-- Only widens a CHECK; an older binary never writes 'user' and keeps working.
ALTER TABLE support_chat_messages DROP CONSTRAINT IF EXISTS support_chat_messages_deleted_by_role_check;
ALTER TABLE support_chat_messages ADD CONSTRAINT support_chat_messages_deleted_by_role_check CHECK (deleted_by_role IN ('', 'admin', 'user'));
