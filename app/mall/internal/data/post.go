package data

import (
	"context"
	"errors"
	"time"

	communityv1 "github.com/Dailiduzhou/simple-ecommerce/api/community/v1"
	mediav1 "github.com/Dailiduzhou/simple-ecommerce/api/media/v1"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	"github.com/jackc/pgx/v5"
)

type PostRepo struct {
	data  *Data
	tx    biz.TxManager
	media *MediaRepo
}

func NewPostRepo(d *Data, tx biz.TxManager, m *MediaRepo) *PostRepo { return &PostRepo{d, tx, m} }
func (r *PostRepo) Create(ctx context.Context, a biz.Actor, i biz.PostInput) (out *biz.Post, e error) {
	e = r.tx.InTx(ctx, func(ctx context.Context) error {
		q := r.data.DB(ctx)
		if e := lockCommunityUser(ctx, q, a.ID); e != nil {
			return e
		}
		p, e := q.CreatePost(ctx, db.CreatePostParams{AuthorID: communityID(a.ID), Title: i.Title, Content: i.Content})
		if e != nil {
			return e
		}
		if e = r.replaceImages(ctx, a.ID, p.ID, i.ImageIDs); e != nil {
			return e
		}
		out, e = r.Get(ctx, a.ID, p.ID)
		return e
	})
	return
}

func (r *PostRepo) Get(ctx context.Context, viewer, id int64) (out *biz.Post, err error) {
	err = r.readSnapshot(ctx, func(ctx context.Context) error {
		out, err = r.get(ctx, viewer, id)
		return err
	})
	return
}

func (r *PostRepo) get(ctx context.Context, viewer, id int64) (*biz.Post, error) {
	p, e := r.data.DB(ctx).GetVisiblePost(ctx, id)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, communityv1.ErrorPostNotFound("post not found")
	}
	if e != nil {
		return nil, e
	}
	out := []biz.Post{{ID: p.ID, Author: biz.Author{ID: p.AuthorID.Int64, Nickname: p.Nickname}, Title: p.Title, Content: p.Content, Version: p.Version, CreatedAt: p.CreatedAt.Time, UpdatedAt: p.UpdatedAt.Time}}
	if e = r.enrich(ctx, viewer, out); e != nil {
		return nil, e
	}
	return &out[0], nil
}

func (r *PostRepo) List(ctx context.Context, viewer, author int64, page biz.Page) (out []biz.Post, next string, err error) {
	err = r.readSnapshot(ctx, func(ctx context.Context) error {
		out, next, err = r.list(ctx, viewer, author, page)
		return err
	})
	return
}

func (r *PostRepo) list(ctx context.Context, viewer, author int64, page biz.Page) ([]biz.Post, string, error) {
	rows, e := r.data.DB(ctx).ListPosts(ctx, db.ListPostsParams{AuthorID: author, HasCursor: page.HasCursor, CursorTime: communityTime(page.Time), CursorID: page.ID, PageLimit: page.Limit + 1})
	if e != nil {
		return nil, "", e
	}
	next := ""
	if len(rows) > int(page.Limit) {
		rows = rows[:page.Limit]
		last := rows[len(rows)-1]
		next = biz.NextCursor(page, last.CreatedAt.Time, last.ID)
	}
	out := make([]biz.Post, 0, len(rows))
	for _, p := range rows {
		out = append(out, biz.Post{ID: p.ID, Author: biz.Author{ID: p.AuthorID.Int64, Nickname: p.Nickname}, Title: p.Title, Content: p.Content, Version: p.Version, CreatedAt: p.CreatedAt.Time, UpdatedAt: p.UpdatedAt.Time})
	}
	if e = r.enrich(ctx, viewer, out); e != nil {
		return nil, "", e
	}
	return out, next, nil
}

// Three page-wide queries, regardless of the number of posts, authors or images.
func (r *PostRepo) enrich(ctx context.Context, viewer int64, posts []biz.Post) error {
	if len(posts) == 0 {
		return nil
	}
	ids := make([]int64, len(posts))
	byID := make(map[int64]*biz.Post, len(posts))
	for i := range posts {
		ids[i] = posts[i].ID
		byID[posts[i].ID] = &posts[i]
	}
	q := r.data.DB(ctx)
	stats, e := q.GetPostStats(ctx, db.GetPostStatsParams{ViewerID: viewer, PostIds: ids})
	if e != nil {
		return e
	}
	for _, s := range stats {
		p := byID[s.ID]
		p.LikeCount = s.LikeCount
		p.CommentCount = s.CommentCount
		p.LikedByMe = s.LikedByMe
	}
	images, e := q.GetPostImages(ctx, ids)
	if e != nil {
		return e
	}
	for _, m := range images {
		p := byID[m.PostID]
		p.Images = append(p.Images, biz.MediaAsset{ID: m.ID, OwnerID: m.OwnerID.Int64, Object: biz.ObjectRef{Provider: m.Provider, Bucket: m.BucketName, Key: m.ObjectKey}, ContentType: m.ContentType, SizeBytes: m.SizeBytes, Width: m.Width, Height: m.Height, Status: m.Status})
	}
	return nil
}

func (r *PostRepo) Update(ctx context.Context, a biz.Actor, id int64, i biz.PostInput) (out *biz.Post, e error) {
	e = r.tx.InTx(ctx, func(ctx context.Context) error {
		q := r.data.DB(ctx)
		if e := lockCommunityUser(ctx, q, a.ID); e != nil {
			return e
		}
		p, e := lockVisiblePost(ctx, q, id)
		if e != nil {
			return e
		}
		if e = a.Owns(p.AuthorID.Int64, false); e != nil {
			return e
		}
		if p.Version != i.ExpectedVersion {
			return communityv1.ErrorPostVersionConflict("refresh the post before editing")
		}
		if e = r.replaceImages(ctx, a.ID, id, i.ImageIDs); e != nil {
			return e
		}
		_, e = q.UpdatePost(ctx, db.UpdatePostParams{ID: id, AuthorID: communityID(a.ID), Title: i.Title, Content: i.Content, ExpectedVersion: i.ExpectedVersion})
		if errors.Is(e, pgx.ErrNoRows) {
			return communityv1.ErrorPostVersionConflict("post version changed")
		}
		if e != nil {
			return e
		}
		out, e = r.Get(ctx, a.ID, id)
		return e
	})
	return
}

// Post lock is held. Lock the union of old/new media IDs in database ID order,
// not in client display order (including on reorder and partial replacement).
func (r *PostRepo) replaceImages(ctx context.Context, uid, postID int64, ids []int64) error {
	q := r.data.DB(ctx)
	old, e := q.GetPostImages(ctx, []int64{postID})
	if e != nil {
		return e
	}
	all := append([]int64{}, ids...)
	for _, m := range old {
		all = append(all, m.ID)
	}
	assets, e := q.LockMediaAssets(ctx, all)
	if e != nil {
		return e
	}
	byID := map[int64]db.MediaAsset{}
	for _, m := range assets {
		byID[m.ID] = m
	}
	keep := map[int64]bool{}
	for _, id := range ids {
		m, ok := byID[id]
		if !ok {
			return mediav1.ErrorMediaNotReady("image not found")
		}
		if m.OwnerID.Int64 != uid || !m.OwnerID.Valid {
			return mediav1.ErrorMediaForbidden("image belongs to another user")
		}
		if m.Status != "ready" {
			return mediav1.ErrorMediaNotReady("image has not been verified")
		}
		bound, e := q.GetMediaBinding(ctx, id)
		if e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		if e == nil && bound != postID {
			return mediav1.ErrorMediaNotReady("image is already bound")
		}
		if errors.Is(e, pgx.ErrNoRows) && !m.ExpiresAt.Time.After(time.Now()) {
			return mediav1.ErrorMediaNotReady("image expired")
		}
		keep[id] = true
	}
	if e = q.UnbindPostImages(ctx, postID); e != nil {
		return e
	}
	for i, id := range ids {
		if e = q.BindPostImage(ctx, db.BindPostImageParams{PostID: postID, MediaID: id, SortOrder: int32(i)}); e != nil {
			return e
		}
	}
	for _, m := range old {
		if !keep[m.ID] {
			if e = r.media.markDeleting(ctx, byID[m.ID]); e != nil {
				return e
			}
		}
	}
	return nil
}

func (r *PostRepo) Delete(ctx context.Context, a biz.Actor, id int64) error {
	return r.tx.InTx(ctx, func(ctx context.Context) error {
		q := r.data.DB(ctx)
		if e := lockCommunityUser(ctx, q, a.ID); e != nil {
			return e
		}
		p, e := q.LockPost(ctx, id)
		if errors.Is(e, pgx.ErrNoRows) {
			return communityv1.ErrorPostNotFound("post not found")
		}
		if e != nil {
			return e
		}
		if e = a.Owns(p.AuthorID.Int64, true); e != nil {
			return e
		}
		if p.DeletedAt.Valid {
			return nil
		}
		images, e := q.LockPostImageAssets(ctx, id)
		if e != nil {
			return e
		}
		if _, e = q.SoftDeletePost(ctx, db.SoftDeletePostParams{ID: id, ActorID: communityID(a.ID), IsAdmin: a.Admin}); e != nil {
			return e
		}
		if e = q.UnbindPostImages(ctx, id); e != nil {
			return e
		}
		for _, m := range images {
			if e = r.media.markDeleting(ctx, m); e != nil {
				return e
			}
		}
		return nil
	})
}

func (r *PostRepo) SetLike(ctx context.Context, a biz.Actor, id int64, on bool) error {
	return r.tx.InTx(ctx, func(ctx context.Context) error {
		q := r.data.DB(ctx)
		if e := lockCommunityUser(ctx, q, a.ID); e != nil {
			return e
		}
		if _, e := lockVisiblePost(ctx, q, id); e != nil {
			return e
		}
		if on {
			return q.LikePost(ctx, db.LikePostParams{PostID: id, UserID: a.ID})
		}
		return q.UnlikePost(ctx, db.UnlikePostParams{PostID: id, UserID: a.ID})
	})
}

// The three bounded page queries must see the same text/version/image state.
// Writes already own their transaction and post locks; never open a nested
// transaction or lose uncommitted images when Get is used to return a write.
func (r *PostRepo) readSnapshot(ctx context.Context, fn func(context.Context) error) error {
	if querierFromContext(ctx, nil) != nil {
		return fn(ctx)
	}
	tx, err := r.data.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	if err = fn(WithQuerier(ctx, db.New(tx), tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
