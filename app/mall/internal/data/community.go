package data

import (
	"context"
	"errors"
	"time"

	communityv1 "github.com/Dailiduzhou/simple-ecommerce/api/community/v1"
	userv1 "github.com/Dailiduzhou/simple-ecommerce/api/user/v1"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/conf"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func communityTime(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }
func communityID(id int64) pgtype.Int8             { return pgtype.Int8{Int64: id, Valid: id > 0} }
func NewCommunityPolicy(c *conf.Community) (biz.CommunityPolicy, error) {
	if e := conf.ValidateCommunity(c); e != nil {
		return biz.CommunityPolicy{}, e
	}
	p := biz.CommunityPolicy{HistoryRetention: 90 * 24 * time.Hour, CleanupBatch: 100}
	if c.GetHistoryRetentionDays() > 0 {
		p.HistoryRetention = time.Duration(c.HistoryRetentionDays) * 24 * time.Hour
	}
	if c.GetCleanupBatchSize() > 0 && c.CleanupBatchSize <= 1000 {
		p.CleanupBatch = c.CleanupBatchSize
	}
	return p, nil
}

func lockCommunityUser(ctx context.Context, q db.Querier, id int64) error {
	_, e := q.LockCommunityUser(ctx, id)
	if errors.Is(e, pgx.ErrNoRows) {
		return userv1.ErrorUnauthorized("user no longer exists")
	}
	return e
}

func lockVisiblePost(ctx context.Context, q db.Querier, id int64) (db.Post, error) {
	p, e := q.LockPost(ctx, id)
	if errors.Is(e, pgx.ErrNoRows) || (e == nil && p.DeletedAt.Valid) {
		return p, communityv1.ErrorPostNotFound("post not found")
	}
	return p, e
}
