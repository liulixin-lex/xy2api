package repository

import (
	"context"

	"github.com/lib/pq"
	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
	"github.com/liulixin-lex/xy2api/internal/service"
)

var oauthOnlyGroupPlatforms = []string{service.PlatformOpenAI, service.PlatformAntigravity, service.PlatformAnthropic, service.PlatformGemini, service.PlatformGrok, service.PlatformComposite}

// The caller holds the account row and live group locks in that order. Read
// membership only now: a stale service snapshot cannot exempt a detached member.
func validateNewAccountGroupBindings(ctx context.Context, exec sqlExecutor, accountID int64, groupIDs []int64) error {
	if len(groupIDs) == 0 {
		return nil
	}
	var mismatch bool
	err := scanSingleRow(ctx, exec, `SELECT EXISTS (
 SELECT 1 FROM groups g JOIN accounts a ON a.id=$1
 WHERE g.id=ANY($2::bigint[]) AND g.require_oauth_only AND g.platform=ANY($3::text[]) AND a.type=$4
 AND NOT EXISTS (SELECT 1 FROM account_groups ag WHERE ag.account_id=a.id AND ag.group_id=g.id)
 )`, []any{accountID, pq.Array(groupIDs), pq.Array(oauthOnlyGroupPlatforms), service.AccountTypeAPIKey}, &mismatch)
	if err != nil {
		return err
	}
	if mismatch {
		return infraerrors.BadRequest("GROUP_OAUTH_ONLY", "OAuth-only groups cannot accept new API-key account bindings")
	}
	return nil
}

// Copying members keeps its filtering behavior. The candidate identities/pins
// are frozen already, but account types and the target flag must be read only
// after both sets of rows are locked.
func filterOAuthOnlyCopyMembers(ctx context.Context, exec sqlExecutor, groupID int64, snapshot *memberCopySnapshot, oauthOnly bool) error {
	if len(snapshot.sourceIDs) == 0 {
		return nil
	}
	rows, err := exec.QueryContext(ctx, `SELECT a.id FROM accounts a JOIN groups g ON g.id=$1
 WHERE a.id=ANY($2::bigint[]) AND (NOT ($3 OR (g.require_oauth_only AND g.platform=ANY($4::text[]))) OR a.type<>$5)`, groupID, pq.Array(snapshot.sourceIDs), oauthOnly, pq.Array(oauthOnlyGroupPlatforms), service.AccountTypeAPIKey)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	allowed := make(map[int64]bool, len(snapshot.sourceIDs))
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return err
		}
		allowed[id] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	count := 0
	for i, id := range snapshot.sourceIDs {
		if allowed[id] {
			snapshot.sourceIDs[count] = id
			snapshot.sourcePinned[count] = snapshot.sourcePinned[i]
			count++
		}
	}
	snapshot.sourceIDs = snapshot.sourceIDs[:count]
	snapshot.sourcePinned = snapshot.sourcePinned[:count]
	return nil
}
