package data

import (
	"context"
	"errors"
	"time"

	userv1 "github.com/Dailiduzhou/simple-ecommerce/api/user/v1"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	"github.com/jackc/pgx/v5"
)

type BrowsingHistoryRepo struct {
	data   *Data
	tx     biz.TxManager
	policy biz.CommunityPolicy
}

func NewBrowsingHistoryRepo(d *Data, tx biz.TxManager, p biz.CommunityPolicy) *BrowsingHistoryRepo {
	return &BrowsingHistoryRepo{d, tx, p}
}

func (r *BrowsingHistoryRepo) write(ctx context.Context, uid int64, fn func(context.Context, db.Querier) error) error {
	return r.tx.InTx(ctx, func(ctx context.Context) error {
		q := r.data.DB(ctx)
		if e := lockCommunityUser(ctx, q, uid); e != nil {
			return e
		}
		return fn(ctx, q)
	})
}

func (r *BrowsingHistoryRepo) Record(ctx context.Context, uid, id int64) (t time.Time, e error) {
	e = r.write(ctx, uid, func(ctx context.Context, q db.Querier) error {
		h, e := q.RecordProductView(ctx, db.RecordProductViewParams{UserID: uid, ProductID: id})
		if errors.Is(e, pgx.ErrNoRows) {
			return userv1.ErrorProductNotAvailable("product is not available")
		}
		t = h.LastViewedAt.Time
		return e
	})
	return
}

func (r *BrowsingHistoryRepo) List(ctx context.Context, uid int64, p biz.Page, f biz.HistoryFilter) ([]biz.BrowsingHistoryItem, string, error) {
	rows, e := r.data.DB(ctx).ListBrowsingHistory(ctx, db.ListBrowsingHistoryParams{UserID: uid, Cutoff: communityTime(time.Now().Add(-r.policy.HistoryRetention)), HasStart: f.HasStart, StartTime: communityTime(f.Start), HasEnd: f.HasEnd, EndTime: communityTime(f.End), HasCursor: p.HasCursor, CursorTime: communityTime(p.Time), CursorID: p.ID, PageLimit: p.Limit + 1})
	if e != nil {
		return nil, "", e
	}
	next := ""
	if len(rows) > int(p.Limit) {
		rows = rows[:p.Limit]
		last := rows[len(rows)-1]
		next = biz.NextCursor(p, last.LastViewedAt.Time, last.ProductID)
	}
	out := make([]biz.BrowsingHistoryItem, 0, len(rows))
	for _, h := range rows {
		item := biz.BrowsingHistoryItem{ProductID: h.ProductID, Name: h.Name, PriceMinor: h.PriceMinor, CoverImageJSON: string(h.CoverImage), Available: h.Available, LastViewedAt: h.LastViewedAt.Time}
		if !item.Available {
			item.PriceMinor = 0
		}
		out = append(out, item)
	}
	return out, next, nil
}

func (r *BrowsingHistoryRepo) Delete(ctx context.Context, uid, id int64) error {
	return r.write(ctx, uid, func(ctx context.Context, q db.Querier) error {
		return q.DeleteBrowsingHistoryItem(ctx, db.DeleteBrowsingHistoryItemParams{UserID: uid, ProductID: id})
	})
}

func (r *BrowsingHistoryRepo) Clear(ctx context.Context, uid int64) error {
	return r.write(ctx, uid, func(ctx context.Context, q db.Querier) error { return q.ClearBrowsingHistory(ctx, uid) })
}

func (r *BrowsingHistoryRepo) Cleanup(ctx context.Context) (int64, error) {
	return r.data.DB(ctx).CleanupBrowsingHistory(ctx, db.CleanupBrowsingHistoryParams{Cutoff: communityTime(time.Now().Add(-r.policy.HistoryRetention)), BatchSize: r.policy.CleanupBatch})
}
