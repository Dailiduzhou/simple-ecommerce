package data

import (
	"context"
	"errors"
	"fmt"
	"time"

	mediav1 "github.com/Dailiduzhou/simple-ecommerce/api/media/v1"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
)

type MediaRepo struct {
	data   *Data
	tx     biz.TxManager
	jobs   *RiverInsertClient
	policy biz.CommunityPolicy
}

func NewMediaRepo(d *Data, tx biz.TxManager, j *RiverInsertClient, p biz.CommunityPolicy) *MediaRepo {
	return &MediaRepo{d, tx, j, p}
}

func toMedia(m db.MediaAsset) *biz.MediaAsset {
	return &biz.MediaAsset{ID: m.ID, OwnerID: m.OwnerID.Int64, Object: biz.ObjectRef{Provider: m.Provider, Bucket: m.BucketName, Key: m.ObjectKey}, StagingKey: m.StagingKey, ContentType: m.ContentType, SizeBytes: m.SizeBytes, Width: m.Width, Height: m.Height, Status: m.Status, ExpiresAt: m.ExpiresAt.Time, UploadExpiresAt: m.UploadExpiresAt.Time}
}

func (r *MediaRepo) Create(ctx context.Context, uid int64, m biz.MediaAsset) (out *biz.MediaAsset, e error) {
	e = r.tx.InTx(ctx, func(ctx context.Context) error {
		q := r.data.DB(ctx)
		if e := lockCommunityUser(ctx, q, uid); e != nil {
			return e
		}
		row, e := q.CreateMediaAsset(ctx, db.CreateMediaAssetParams{OwnerID: communityID(uid), Provider: m.Object.Provider, BucketName: m.Object.Bucket, ObjectKey: m.Object.Key, StagingKey: m.StagingKey, ContentType: m.ContentType, SizeBytes: m.SizeBytes, ExpiresAt: communityTime(m.ExpiresAt), UploadExpiresAt: communityTime(m.UploadExpiresAt)})
		out = toMedia(row)
		return e
	})
	return
}

// A session advisory lock serializes completion and deletion across processes.
// The lock is AcquireMediaIOLock/ReleaseMediaIOLock (namespace 1279476052, key
// hashint8(media id)) in db/query/media_assets.sql. No SQL transaction spans
// object storage I/O. If cleanup marks deleting while verification runs, its
// worker waits here, then removes every possible object. This also closes the
// "delete succeeded, late PUT resurrected the object" race.
func (r *MediaRepo) withIOLock(ctx context.Context, id int64, fn func(context.Context, db.Querier) error) error {
	conn, e := r.data.pool.Acquire(ctx)
	if e != nil {
		return e
	}
	defer conn.Release()
	q := db.New(conn)
	if e = q.AcquireMediaIOLock(ctx, id); e != nil {
		_ = conn.Conn().Close(context.Background())
		return e
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		ok, e := q.ReleaseMediaIOLock(cleanup, id)
		if e != nil || !ok {
			_ = conn.Conn().Close(cleanup)
		}
	}()
	ioCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	return fn(ioCtx, q)
}

func (r *MediaRepo) Complete(ctx context.Context, uid, id int64, verify func(context.Context, biz.MediaAsset) (biz.VerifiedImage, error)) (out *biz.MediaAsset, e error) {
	e = r.withIOLock(ctx, id, func(ctx context.Context, q db.Querier) error {
		m, e := q.GetMediaAsset(ctx, id)
		if errors.Is(e, pgx.ErrNoRows) {
			return mediav1.ErrorMediaNotFound("image not found")
		}
		if e != nil {
			return e
		}
		if !m.OwnerID.Valid || m.OwnerID.Int64 != uid {
			return mediav1.ErrorMediaForbidden("image belongs to another user")
		}
		if m.Status == "ready" {
			allowed, e := q.CanReadMedia(ctx, db.CanReadMediaParams{ID: id, ViewerID: communityID(uid)})
			if e != nil {
				return e
			}
			if !allowed {
				return mediav1.ErrorMediaNotReady("image expired")
			}
			out = toMedia(m)
			return nil
		}
		if m.Status != "pending" || !m.ExpiresAt.Time.After(time.Now()) {
			return mediav1.ErrorMediaNotReady("image is expired or being deleted")
		}
		v, e := verify(ctx, *toMedia(m))
		if e != nil {
			return e
		}
		m, e = q.ReadyMediaAsset(ctx, db.ReadyMediaAssetParams{ID: id, ContentType: v.ContentType, SizeBytes: v.Size, Width: v.Width, Height: v.Height})
		if errors.Is(e, pgx.ErrNoRows) {
			return mediav1.ErrorMediaNotReady("image cleanup already started")
		}
		if e != nil {
			return e
		}
		out = toMedia(m)
		return nil
	})
	return
}

func (r *MediaRepo) Read(ctx context.Context, uid, id int64) (*biz.MediaAsset, error) {
	q := r.data.DB(ctx)
	m, e := q.GetMediaAsset(ctx, id)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, mediav1.ErrorMediaNotFound("image not found")
	}
	if e != nil {
		return nil, e
	}
	ok, e := q.CanReadMedia(ctx, db.CanReadMediaParams{ID: id, ViewerID: communityID(uid)})
	if e != nil {
		return nil, e
	}
	if !ok || m.Status != "ready" {
		return nil, mediav1.ErrorMediaNotFound("image is not visible")
	}
	return toMedia(m), nil
}

func (r *MediaRepo) enqueueDelete(ctx context.Context, m db.MediaAsset) error {
	if r.jobs == nil || r.jobs.client == nil || r.data.GetPgTx(ctx) == nil {
		return fmt.Errorf("media deletion requires a transactional River client")
	}
	// A live POST policy can recreate the staging key; do not delete it until the
	// last credential has expired, including clock/network grace.
	due := time.Now().Add(time.Minute)
	if t := m.UploadExpiresAt.Time.Add(time.Minute); t.After(due) {
		due = t
	}
	_, e := r.jobs.client.InsertTx(ctx, r.data.GetPgTx(ctx), biz.MediaDeleteArgs{MediaID: m.ID}, &river.InsertOpts{Queue: "media", MaxAttempts: 10, ScheduledAt: due, UniqueOpts: river.UniqueOpts{ByArgs: true}})
	return e
}

func (r *MediaRepo) markDeleting(ctx context.Context, m db.MediaAsset) error {
	n, e := r.data.DB(ctx).MarkMediaDeleting(ctx, m.ID)
	if e != nil {
		return e
	}
	if n == 0 {
		return nil
	}
	return r.enqueueDelete(ctx, m)
}

func (r *MediaRepo) Sweep(ctx context.Context) (n int, e error) {
	e = r.tx.InTx(ctx, func(ctx context.Context) error {
		rows, e := r.data.DB(ctx).LockExpiredMedia(ctx, r.policy.CleanupBatch)
		if e != nil {
			return e
		}
		for _, row := range rows {
			m := db.MediaAsset(row)
			if m.Status == "deleting" {
				e = r.enqueueDelete(ctx, m)
				if e == nil {
					e = r.data.DB(ctx).TouchDeletingMedia(ctx, m.ID)
				}
			} else {
				e = r.markDeleting(ctx, m)
			}
			if e != nil {
				return e
			}
			n++
		}
		return nil
	})
	return
}

func (r *MediaRepo) Remove(ctx context.Context, id int64, remove func(context.Context, biz.MediaAsset) error) error {
	return r.withIOLock(ctx, id, func(ctx context.Context, q db.Querier) error {
		m, e := q.GetMediaAsset(ctx, id)
		if errors.Is(e, pgx.ErrNoRows) {
			return nil
		}
		if e != nil {
			return e
		}
		if m.Status == "deleted" {
			return nil
		}
		if m.Status != "deleting" {
			return mediav1.ErrorMediaNotReady("only deleting resources can be removed")
		}
		if time.Now().Before(m.UploadExpiresAt.Time.Add(time.Minute)) {
			return biz.ErrMediaCleanupNotDue
		}
		_, e = q.GetMediaBinding(ctx, id)
		if e == nil {
			return mediav1.ErrorMediaNotReady("referenced images cannot be deleted")
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		if e = remove(ctx, *toMedia(m)); e != nil {
			return e
		}
		return q.MarkMediaDeleted(ctx, id)
	})
}
