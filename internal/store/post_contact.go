package store

import (
	"context"
	"time"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
)

type (
	PostContactTarget struct {
		Title    string `db:"title"`
		AuthorID *int64 `db:"author_id"`
	}

	// PostContactRequest retains only the Post and message needed for contact.
	// AuthorID is private and used solely to authorize delivery and replies.
	PostContactRequest struct {
		ID                string    `db:"id"`
		PostID            int64     `db:"post_id"`
		PostTitle         string    `db:"post_title"`
		RequesterID       int64     `db:"requester_id"`
		AuthorID          *int64    `db:"author_id"`
		Status            string    `db:"status"`
		Intro             *string   `db:"intro"`
		ConversationID    *int64    `db:"conversation_id"`
		CreatedAt         time.Time `db:"created_at"`
		RequesterAddress  *string   `db:"requester_address"`
		RequesterUsername *string   `db:"requester_username"`
		RequesterAvatar   *string   `db:"requester_avatar"`
	}
)

func (pg *Pg) PostContactTarget(ctx context.Context, tx pgx.Tx, postID int64) (PostContactTarget, error) {
	var target PostContactTarget
	err := pgxscan.Get(ctx, tx, &target, pg.queries.PostContactTarget, postID)
	return target, err
}

func (pg *Pg) PostContactForRequester(ctx context.Context, tx pgx.Tx, requesterID, postID int64) (PostContactRequest, error) {
	var request PostContactRequest
	err := pgxscan.Get(ctx, tx, &request, pg.queries.PostContactForRequester, requesterID, postID)
	return request, err
}

func (pg *Pg) InsertPostContact(ctx context.Context, tx pgx.Tx, id string, requesterID, postID int64, title string, intro *string) (PostContactRequest, error) {
	var request PostContactRequest
	err := pgxscan.Get(ctx, tx, &request, pg.queries.PostContactInsert, id, requesterID, postID, title, intro)
	return request, err
}

func (pg *Pg) AssignPostContactAuthor(ctx context.Context, tx pgx.Tx, id string, authorID int64) error {
	_, err := tx.Exec(ctx, pg.queries.PostContactAssignAuthor, id, authorID)
	return err
}

func (pg *Pg) ListPostContacts(ctx context.Context, tx pgx.Tx, viewer int64, after *string, limit int) ([]PostContactRequest, error) {
	var rows []PostContactRequest
	err := pgxscan.Select(ctx, tx, &rows, pg.queries.PostContactList, viewer, after, limit)
	return rows, err
}

func (pg *Pg) LockPostContactForAuthor(ctx context.Context, tx pgx.Tx, id string, authorID int64) (PostContactRequest, error) {
	var request PostContactRequest
	err := pgxscan.Get(ctx, tx, &request, pg.queries.PostContactLockAuthor, id, authorID)
	return request, err
}

func (pg *Pg) LockDeliverablePostContact(ctx context.Context, tx pgx.Tx, id string) error {
	var postID int64
	return tx.QueryRow(ctx, pg.queries.PostContactLockDeliverable, id).Scan(&postID)
}

func (pg *Pg) SetPostContactStatus(ctx context.Context, tx pgx.Tx, id, status string, conversationID *int64) error {
	_, err := tx.Exec(ctx, pg.queries.PostContactSetStatus, id, status, conversationID)
	return err
}
