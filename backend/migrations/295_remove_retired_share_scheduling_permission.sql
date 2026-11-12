-- Remove the retired module so stored operator permissions remain decodable.
UPDATE admin_operator_permissions
SET permissions = permissions #- '{modules,share_scheduling}'
WHERE permissions -> 'modules' ? 'share_scheduling';
