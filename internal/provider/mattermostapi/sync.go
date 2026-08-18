package mattermostapi

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"time"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/domain"
)

// SyncMessages performs a bounded, anchor-paginated snapshot. Each new cycle
// explicitly resets the account-local cache; a WebSocket invalidation can
// therefore only cause an earlier snapshot, never become trusted message data.
func (client *Client) SyncMessages(
	ctx context.Context,
	input application.MessageSyncInput,
) (application.MessageChangePage, error) {
	if err := client.requireCapability(client.capabilities.IncrementalSync, "message synchronization"); err != nil {
		return application.MessageChangePage{}, err
	}
	if input.WorkspaceID != client.workspaceID || !validMattermostID(input.ConversationID) {
		return application.MessageChangePage{}, errors.New("mattermost synchronization requires one exact conversation")
	}
	if _, err := client.getMattermostConversation(ctx, input.ConversationID); err != nil {
		return application.MessageChangePage{}, err
	}
	before := ""
	reset := true
	if input.Cursor != nil {
		cursor, err := decodeMattermostCursor(input.Cursor.Opaque, mattermostCursor{
			Kind: mattermostCursorSync, Account: input.Account,
			WorkspaceID: input.WorkspaceID, ConversationID: input.ConversationID,
		})
		if err != nil {
			return application.MessageChangePage{}, err
		}
		before = cursor.Before
		reset = before == ""
	}
	limit := min(input.Limit, application.MaxMessagePageSize)
	query := url.Values{"per_page": {strconv.Itoa(limit + 1)}}
	if before != "" {
		query.Set("before", before)
	}
	posts, err := client.getPostPage(
		ctx, "channels/"+input.ConversationID+"/posts", query, limit+1,
	)
	if err != nil {
		return application.MessageChangePage{}, err
	}
	hasMore := len(posts) > limit
	if hasMore {
		posts = posts[:limit]
	}
	changes := make([]application.MessageChange, 0, len(posts))
	for _, post := range posts {
		summary, err := mapMattermostSummary(post, input.ConversationID)
		if err != nil {
			return application.MessageChangePage{}, err
		}
		changes = append(changes, application.MessageChange{
			Kind: application.MessageChangeUpsert, Message: &summary,
		})
	}
	nextBefore := ""
	if hasMore && len(posts) != 0 {
		nextBefore = posts[len(posts)-1].ID
	}
	opaque, err := encodeMattermostCursor(mattermostCursor{
		Version: 1, Kind: mattermostCursorSync, Account: input.Account,
		WorkspaceID: input.WorkspaceID, ConversationID: input.ConversationID,
		Before: nextBefore,
	})
	if err != nil {
		return application.MessageChangePage{}, err
	}
	return application.MessageChangePage{
		Changes: changes,
		Cursor: application.MessageCursor{
			Version: 1, Account: input.Account, Provider: domain.MessagingProviderMattermost,
			Route: domain.MessagingRouteMattermost, WorkspaceID: input.WorkspaceID,
			ConversationID: input.ConversationID, Opaque: opaque,
		},
		HasMore: hasMore, Reset: reset, ObservedAt: time.Now().UTC(),
	}, nil
}
