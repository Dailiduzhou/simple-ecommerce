//go:build integration

package data

import (
	"context"
	"errors"
	"testing"
	"time"

	communityv1 "github.com/Dailiduzhou/simple-ecommerce/api/community/v1"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/stretchr/testify/require"
)

type postBoundaryStorage struct {
	biz.ObjectStorage
	sign func(biz.ObjectRef) (string, error)
}

func (s postBoundaryStorage) ReadURL(_ context.Context, ref biz.ObjectRef, _ time.Duration) (string, error) {
	return s.sign(ref)
}

func TestPostSigningFailureLeavesDatabaseUnchangedIntegration(t *testing.T) {
	f := newCommunityFixture(t)
	originalImage := f.asset(t, f.actor.ID, "ready")
	nextImage := f.asset(t, f.actor.ID, "ready")
	p := f.post(t, f.actor, originalImage)
	want := errors.New("injected signing failure")
	calls := 0
	u := biz.NewPostUsecase(f.posts, postBoundaryStorage{sign: func(biz.ObjectRef) (string, error) { calls++; return "", want }}, biz.MediaPolicy{ReadTTL: time.Minute})
	assertUnchanged := func() {
		var count int
		require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT count(*) FROM posts WHERE author_id=$1`, f.actor.ID).Scan(&count))
		require.Equal(t, 1, count)
		current, err := f.posts.Get(f.ctx, f.actor.ID, p.ID)
		require.NoError(t, err)
		require.Equal(t, p.Version, current.Version)
		require.Equal(t, p.Title, current.Title)
		require.Equal(t, p.Content, current.Content)
		require.Len(t, current.Images, 1)
		require.Equal(t, originalImage, current.Images[0].ID)
		var bound bool
		require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT EXISTS(SELECT 1 FROM post_images WHERE media_id=$1)`, nextImage).Scan(&bound))
		require.False(t, bound)
	}
	result, err := u.Create(f.ctx, f.actor, biz.PostInput{Title: "new", Content: "new", ImageIDs: []int64{nextImage}})
	require.ErrorIs(t, err, want)
	require.Nil(t, result)
	assertUnchanged()
	result, err = u.Update(f.ctx, f.actor, p.ID, biz.PostInput{Title: "new", Content: "new", ImageIDs: []int64{nextImage}, ExpectedVersion: p.Version})
	require.ErrorIs(t, err, want)
	require.Nil(t, result)
	assertUnchanged()
	require.Equal(t, 2, calls)
}

func TestPostSigningPreparationRevalidatedInsideWriteIntegration(t *testing.T) {
	for _, mutation := range []string{"deleting", "expired", "other owner", "bound elsewhere", "provider", "bucket", "key", "post version"} {
		t.Run(mutation, func(t *testing.T) {
			f := newCommunityFixture(t)
			originalImage := f.asset(t, f.actor.ID, "ready")
			candidate := f.asset(t, f.actor.ID, "ready")
			p := f.post(t, f.actor, originalImage)
			var committedVersion = p.Version
			// This deterministic seam is after the advisory pre-read, before the
			// write transaction. No timing/sleep-dependent race is needed.
			storage := postBoundaryStorage{sign: func(biz.ObjectRef) (string, error) {
				var err error
				switch mutation {
				case "deleting":
					_, err = f.pool.Exec(f.ctx, `UPDATE media_assets SET status='deleting' WHERE id=$1`, candidate)
				case "expired":
					_, err = f.pool.Exec(f.ctx, `UPDATE media_assets SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, candidate)
				case "other owner":
					_, err = f.pool.Exec(f.ctx, `UPDATE media_assets SET owner_id=$2 WHERE id=$1`, candidate, f.other.ID)
				case "bound elsewhere":
					f.post(t, f.actor, candidate)
				case "provider":
					_, err = f.pool.Exec(f.ctx, `UPDATE media_assets SET provider='changed' WHERE id=$1`, candidate)
				case "bucket":
					_, err = f.pool.Exec(f.ctx, `UPDATE media_assets SET bucket_name='changed' WHERE id=$1`, candidate)
				case "key":
					_, err = f.pool.Exec(f.ctx, `UPDATE media_assets SET object_key=object_key||'-changed' WHERE id=$1`, candidate)
				case "post version":
					_, err = f.posts.Update(f.ctx, f.actor, p.ID, biz.PostInput{Title: p.Title, Content: p.Content, ImageIDs: []int64{originalImage}, ExpectedVersion: p.Version})
					committedVersion++
				}
				require.NoError(t, err)
				return "prepared-but-never-returned", nil
			}}
			u := biz.NewPostUsecase(f.posts, storage, biz.MediaPolicy{ReadTTL: time.Minute})
			result, err := u.Update(f.ctx, f.actor, p.ID, biz.PostInput{Title: "must not commit", Content: "must not commit", ImageIDs: []int64{candidate}, ExpectedVersion: p.Version})
			require.Error(t, err)
			require.Nil(t, result)
			if mutation == "post version" {
				require.True(t, communityv1.IsPostVersionConflict(err))
			}
			current, err := f.posts.Get(f.ctx, f.actor.ID, p.ID)
			require.NoError(t, err)
			require.Equal(t, committedVersion, current.Version)
			require.Equal(t, p.Title, current.Title)
			require.Equal(t, p.Content, current.Content)
			require.Len(t, current.Images, 1)
			require.Equal(t, originalImage, current.Images[0].ID)
		})
	}
}

func TestPostSigningPreparationAuthorizesBeforeSigningIntegration(t *testing.T) {
	f := newCommunityFixture(t)
	p := f.post(t, f.actor)
	cases := []struct {
		actor  biz.Actor
		postID int64
		input  biz.PostInput
	}{
		{f.other, p.ID, biz.PostInput{Title: "x", Content: "x", ImageIDs: []int64{p.Images[0].ID}, ExpectedVersion: p.Version}},
		{f.actor, 0, biz.PostInput{Title: "x", Content: "x", ImageIDs: []int64{f.asset(t, f.other.ID, "ready")}}},
		{f.actor, 0, biz.PostInput{Title: "x", Content: "x", ImageIDs: []int64{f.asset(t, f.actor.ID, "pending")}}},
		{f.actor, 0, biz.PostInput{Title: "x", Content: "x", ImageIDs: []int64{p.Images[0].ID}}},
	}
	u := biz.NewPostUsecase(f.posts, postBoundaryStorage{sign: func(biz.ObjectRef) (string, error) {
		t.Fatal("unauthorized metadata must never reach the signer")
		return "", nil
	}}, biz.MediaPolicy{ReadTTL: time.Minute})
	for _, tc := range cases {
		var err error
		if tc.postID == 0 {
			_, err = u.Create(f.ctx, tc.actor, tc.input)
		} else {
			_, err = u.Update(f.ctx, tc.actor, tc.postID, tc.input)
		}
		require.Error(t, err)
	}
}

func TestPostSigningCreateRechecksBindingAndRollsBackIntegration(t *testing.T) {
	f := newCommunityFixture(t)
	candidate := f.asset(t, f.actor.ID, "ready")
	storage := postBoundaryStorage{sign: func(biz.ObjectRef) (string, error) {
		// Another publication wins after pre-reading the media, before Create's
		// transaction. The losing publication must leave no orphan post.
		f.post(t, f.actor, candidate)
		return "never-return-this-link", nil
	}}
	u := biz.NewPostUsecase(f.posts, storage, biz.MediaPolicy{ReadTTL: time.Minute})
	post, err := u.Create(f.ctx, f.actor, biz.PostInput{Title: "loser", Content: "loser", ImageIDs: []int64{candidate}})
	require.Error(t, err)
	require.Nil(t, post)
	var count int
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT count(*) FROM posts WHERE author_id=$1`, f.actor.ID).Scan(&count))
	require.Equal(t, 1, count)
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT count(*) FROM post_images WHERE media_id=$1`, candidate).Scan(&count))
	require.Equal(t, 1, count)
}

func TestPostSigningPreparationRetainsExpiredBoundImageIntegration(t *testing.T) {
	f := newCommunityFixture(t)
	p := f.post(t, f.actor)
	_, err := f.pool.Exec(f.ctx, `UPDATE media_assets SET expires_at=clock_timestamp()-interval '1 day' WHERE id=$1`, p.Images[0].ID)
	require.NoError(t, err)
	calls := 0
	u := biz.NewPostUsecase(f.posts, postBoundaryStorage{sign: func(ref biz.ObjectRef) (string, error) { calls++; return "signed/" + ref.Key, nil }}, biz.MediaPolicy{ReadTTL: time.Minute})
	updated, err := u.Update(f.ctx, f.actor, p.ID, biz.PostInput{Title: "updated", Content: "updated", ImageIDs: []int64{p.Images[0].ID}, ExpectedVersion: p.Version})
	require.NoError(t, err)
	require.Equal(t, p.Version+1, updated.Version)
	require.Equal(t, 1, calls)
	require.Equal(t, "signed/"+p.Images[0].Object.Key, updated.Images[0].URL)
}
