package data

import (
	"context"
	stderrors "errors"

	communityv1 "github.com/Dailiduzhou/simple-ecommerce/api/community/v1"
	mediav1 "github.com/Dailiduzhou/simple-ecommerce/api/media/v1"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	"github.com/jackc/pgx/v5"
)

// PrepareImages authorizes the pre-signing read, without opening or holding a
// write transaction during object storage I/O. It is not a binding guarantee.
func (r *PostRepo) PrepareImages(ctx context.Context, actor biz.Actor, postID int64, input biz.PostInput) ([]biz.MediaAsset, error) {
	if err := actor.Validate(); err != nil {
		return nil, err
	}
	validated, err := input.Validate()
	if err != nil {
		return nil, err
	}
	q := r.data.DB(ctx)
	if postID != 0 {
		post, err := q.GetVisiblePost(ctx, postID)
		if stderrors.Is(err, pgx.ErrNoRows) {
			return nil, communityv1.ErrorPostNotFound("post not found")
		}
		if err != nil {
			return nil, err
		}
		if err := actor.Owns(post.AuthorID.Int64, false); err != nil {
			return nil, err
		}
		if post.Version != input.ExpectedVersion {
			return nil, communityv1.ErrorPostVersionConflict("refresh the post before editing")
		}
	}
	if len(validated.ImageIDs) == 0 {
		return nil, nil
	}
	assets, err := q.GetPostImageCandidates(ctx, db.GetPostImageCandidatesParams{Ids: validated.ImageIDs, OwnerID: communityID(actor.ID), PostID: postID})
	if err != nil {
		return nil, err
	}
	if len(assets) != len(validated.ImageIDs) {
		return nil, mediav1.ErrorMediaNotReady("image unavailable for this post")
	}
	out := make([]biz.MediaAsset, 0, len(assets))
	for _, asset := range assets {
		out = append(out, *toMedia(asset))
	}
	return out, nil
}
