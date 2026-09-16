package biz

import (
	"context"
	"time"

	userv1 "github.com/Dailiduzhou/simple-ecommerce/api/user/v1"
)

type BrowsingHistoryItem struct {
	ProductID          int64
	Name               string
	PriceMinor         int64
	EffectivePriceMinor int64
	CoverImageJSON     string
	Available          bool
	LastViewedAt       time.Time
}

// HistoryFilter is the optional half-open window [Start, End) applied to
// last_viewed_at. HasStart/HasEnd distinguish "filter absent" from the zero
// Time, mirroring the has_cursor pattern used for keyset pagination.
type HistoryFilter struct {
	Start, End       time.Time
	HasStart, HasEnd bool
}

func (f HistoryFilter) Validate() error {
	for _, x := range []struct {
		t   time.Time
		has bool
	}{{f.Start, f.HasStart}, {f.End, f.HasEnd}} {
		if x.has && (x.t.Year() < 1970 || x.t.Year() > 9999) {
			return userv1.ErrorInvalidTimeRange("start_time and end_time years must be between 1970 and 9999")
		}
	}
	if f.HasStart && f.HasEnd && !f.Start.Before(f.End) {
		return userv1.ErrorInvalidTimeRange("start_time must be before end_time")
	}
	// An end_time in the future is harmless: last_viewed_at can never exceed now.
	return nil
}

type BrowsingHistoryRepo interface {
	Record(context.Context, int64, int64) (time.Time, error)
	List(context.Context, int64, Page, HistoryFilter) ([]BrowsingHistoryItem, string, error)
	Delete(context.Context, int64, int64) error
	Clear(context.Context, int64) error
	Cleanup(context.Context) (int64, error)
}
type BrowsingHistoryUsecase struct{ repo BrowsingHistoryRepo }

func NewBrowsingHistoryUsecase(r BrowsingHistoryRepo) *BrowsingHistoryUsecase {
	return &BrowsingHistoryUsecase{r}
}

func (u *BrowsingHistoryUsecase) Record(ctx context.Context, a Actor, id int64) (time.Time, error) {
	if e := a.Validate(); e != nil {
		return time.Time{}, e
	}
	if e := PositiveIDs(id); e != nil {
		return time.Time{}, e
	}
	return u.repo.Record(ctx, a.ID, id)
}

func (u *BrowsingHistoryUsecase) List(ctx context.Context, a Actor, raw string, size int32, f HistoryFilter) ([]BrowsingHistoryItem, string, error) {
	if e := a.Validate(); e != nil {
		return nil, "", e
	}
	if e := f.Validate(); e != nil {
		return nil, "", e
	}
	p, e := ParsePage(raw, Scope("history", a.ID), size)
	if e != nil {
		return nil, "", e
	}
	return u.repo.List(ctx, a.ID, p, f)
}

func (u *BrowsingHistoryUsecase) Delete(ctx context.Context, a Actor, id int64) error {
	if e := a.Validate(); e != nil {
		return e
	}
	if e := PositiveIDs(id); e != nil {
		return e
	}
	return u.repo.Delete(ctx, a.ID, id)
}

func (u *BrowsingHistoryUsecase) Clear(ctx context.Context, a Actor) error {
	if e := a.Validate(); e != nil {
		return e
	}
	return u.repo.Clear(ctx, a.ID)
}

func (u *BrowsingHistoryUsecase) Cleanup(ctx context.Context) (int64, error) {
	return u.repo.Cleanup(ctx)
}
