package data

import (
	"context"
	"errors"

	communityv1 "github.com/Dailiduzhou/simple-ecommerce/api/community/v1"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	"github.com/jackc/pgx/v5"
)

type CommentRepo struct {
	data *Data
	tx   biz.TxManager
}

func NewCommentRepo(d *Data, tx biz.TxManager) *CommentRepo { return &CommentRepo{d, tx} }
func (r *CommentRepo) Create(ctx context.Context, a biz.Actor, postID int64, text string, targetID int64) (out *biz.Comment, e error) {
	e = r.tx.InTx(ctx, func(ctx context.Context) error {
		q := r.data.DB(ctx)
		if e := lockCommunityUser(ctx, q, a.ID); e != nil {
			return e
		}
		if _, e := lockVisiblePost(ctx, q, postID); e != nil {
			return e
		}
		rootID := int64(0)
		if targetID > 0 {
			target, e := q.GetComment(ctx, db.GetCommentParams{PostID: postID, ID: targetID})
			if errors.Is(e, pgx.ErrNoRows) {
				return communityv1.ErrorInvalidReplyTarget("reply target not found in this post")
			}
			if e != nil {
				return e
			}
			if target.DeletedAt.Valid {
				return communityv1.ErrorInvalidReplyTarget("reply target is deleted")
			}
			rootID = target.ID
			if target.RootCommentID.Valid {
				rootID = target.RootCommentID.Int64
			}
			root, e := q.GetComment(ctx, db.GetCommentParams{PostID: postID, ID: rootID})
			if errors.Is(e, pgx.ErrNoRows) {
				return communityv1.ErrorInvalidReplyTarget("invalid root")
			}
			if e != nil {
				return e
			}
			if root.RootCommentID.Valid {
				return communityv1.ErrorInvalidReplyTarget("root must be a top-level comment")
			}
			if root.DeletedAt.Valid {
				return communityv1.ErrorCommentThreadClosed("deleted root closes the thread")
			}
		}
		c, e := q.CreateComment(ctx, db.CreateCommentParams{PostID: postID, AuthorID: communityID(a.ID), RootCommentID: communityID(rootID), ReplyToCommentID: communityID(targetID), Content: text})
		if e != nil {
			return e
		}
		u, e := q.GetUserByID(ctx, a.ID)
		if e != nil {
			return e
		}
		out = &biz.Comment{ID: c.ID, PostID: postID, Author: biz.Author{ID: a.ID, Nickname: u.Nickname}, Content: text, RootID: rootID, ReplyToID: targetID, CreatedAt: c.CreatedAt.Time}
		return nil
	})
	return
}

func (r *CommentRepo) List(ctx context.Context, postID, rootID int64, p biz.Page) ([]biz.Comment, string, error) {
	q := r.data.DB(ctx)
	if _, e := q.GetVisiblePost(ctx, postID); e != nil {
		if errors.Is(e, pgx.ErrNoRows) {
			return nil, "", communityv1.ErrorPostNotFound("post not found")
		}
		return nil, "", e
	}
	out := []biz.Comment{}
	if rootID == 0 {
		rows, e := q.ListRootComments(ctx, db.ListRootCommentsParams{PostID: postID, HasCursor: p.HasCursor, CursorTime: communityTime(p.Time), CursorID: p.ID, PageLimit: p.Limit + 1})
		if e != nil {
			return nil, "", e
		}
		for _, c := range rows {
			item := biz.Comment{ID: c.ID, PostID: postID, Author: biz.Author{ID: c.AuthorID.Int64, Nickname: c.Nickname}, Content: c.Content, Deleted: c.DeletedAt.Valid, CreatedAt: c.CreatedAt.Time, ReplyCount: c.ReplyCount}
			if item.Deleted {
				item.Author = biz.Author{}
				item.Content = ""
			}
			out = append(out, item)
		}
	} else {
		root, e := q.GetComment(ctx, db.GetCommentParams{PostID: postID, ID: rootID})
		if errors.Is(e, pgx.ErrNoRows) || (e == nil && root.RootCommentID.Valid) {
			return nil, "", communityv1.ErrorCommentNotFound("root not found")
		}
		if e != nil {
			return nil, "", e
		}
		rows, e := q.ListCommentReplies(ctx, db.ListCommentRepliesParams{PostID: postID, RootID: communityID(rootID), HasCursor: p.HasCursor, CursorTime: communityTime(p.Time), CursorID: p.ID, PageLimit: p.Limit + 1})
		if e != nil {
			return nil, "", e
		}
		for _, c := range rows {
			item := biz.Comment{ID: c.ID, PostID: postID, Author: biz.Author{ID: c.AuthorID.Int64, Nickname: c.Nickname}, Content: c.Content, CreatedAt: c.CreatedAt.Time, RootID: c.RootCommentID.Int64, ReplyToID: c.ReplyToCommentID.Int64, ReplyToDeleted: c.TargetDeleted}
			if !c.TargetDeleted {
				item.ReplyToAuthor = &biz.Author{ID: c.TargetAuthorID, Nickname: c.TargetNickname}
			}
			out = append(out, item)
		}
	}
	next := ""
	if len(out) > int(p.Limit) {
		out = out[:p.Limit]
		last := out[len(out)-1]
		next = biz.NextCursor(p, last.CreatedAt, last.ID)
	}
	return out, next, nil
}

func (r *CommentRepo) Delete(ctx context.Context, a biz.Actor, postID, id int64) error {
	return r.tx.InTx(ctx, func(ctx context.Context) error {
		q := r.data.DB(ctx)
		if e := lockCommunityUser(ctx, q, a.ID); e != nil {
			return e
		}
		if _, e := lockVisiblePost(ctx, q, postID); e != nil {
			return e
		}
		c, e := q.GetComment(ctx, db.GetCommentParams{PostID: postID, ID: id})
		if errors.Is(e, pgx.ErrNoRows) {
			return communityv1.ErrorCommentNotFound("comment not found")
		}
		if e != nil {
			return e
		}
		if e = a.Owns(c.AuthorID.Int64, true); e != nil {
			return e
		}
		_, e = q.DeleteComment(ctx, db.DeleteCommentParams{PostID: postID, ID: id, ActorID: communityID(a.ID), IsAdmin: a.Admin})
		return e
	})
}
