package data

import (
	"context"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/go-kratos/kratos/v2/log"
)

// CommunityUserRepo preserves the existing user repository and adds only the
// transactional community cleanup required before physical deletion.
type CommunityUserRepo struct {
	*UserRepo
	tx    biz.TxManager
	media *MediaRepo
}

func NewCommunityUserRepo(d *Data, tx biz.TxManager, media *MediaRepo, logger log.Logger) *CommunityUserRepo {
	return &CommunityUserRepo{UserRepo: NewUserRepo(d, logger), tx: tx, media: media}
}

func (r *CommunityUserRepo) DeleteUser(ctx context.Context, id int64) error {
	return r.tx.InTx(ctx, func(ctx context.Context) error {
		q := r.data.DB(ctx)
		if err := lockCommunityUser(ctx, q, id); err != nil {
			return err
		}
		// Every community writer locks its actor first, then posts in ID order.
		// Include posts containing this user's comments before redacting them.
		if _, err := q.LockUserCommunityPosts(ctx, communityID(id)); err != nil {
			return err
		}
		if err := q.HideUserPosts(ctx, communityID(id)); err != nil {
			return err
		}
		if err := q.DeleteUserComments(ctx, communityID(id)); err != nil {
			return err
		}
		media, err := q.LockUserMedia(ctx, communityID(id))
		if err != nil {
			return err
		}
		if err = q.UnbindUserImages(ctx, communityID(id)); err != nil {
			return err
		}
		for _, m := range media {
			if err = r.media.markDeleting(ctx, m); err != nil {
				return err
			}
		}
		// Existing order/payment FKs remain authoritative: any failure rolls back
		// content changes and River inserts, and suppresses cache invalidation.
		return r.UserRepo.DeleteUser(ctx, id)
	})
}

var _ biz.UserRepo = (*CommunityUserRepo)(nil)
