package biz

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type signingPostRepo struct {
	PostRepo
	assets      []MediaAsset
	writes      int
	beforeWrite func(PostInput)
}

func (r *signingPostRepo) PrepareImages(context.Context, Actor, int64, PostInput) ([]MediaAsset, error) {
	return r.assets, nil
}
func (r *signingPostRepo) Create(_ context.Context, a Actor, i PostInput) (*Post, error) {
	r.writes++
	if r.beforeWrite != nil {
		r.beforeWrite(i)
	}
	assets := make([]MediaAsset, 0, len(i.ImageIDs))
	for _, id := range i.ImageIDs {
		for _, asset := range r.assets {
			if asset.ID == id {
				assets = append(assets, asset)
			}
		}
	}
	return &Post{ID: 1, Author: Author{ID: a.ID}, Version: i.ExpectedVersion + 1, Images: assets}, nil
}
func (r *signingPostRepo) Update(ctx context.Context, a Actor, _ int64, i PostInput) (*Post, error) {
	return r.Create(ctx, a, i)
}

type postSigningStorage struct {
	ObjectStorage
	readURL func(context.Context, ObjectRef, time.Duration) (string, error)
}

func (s postSigningStorage) ReadURL(ctx context.Context, ref ObjectRef, ttl time.Duration) (string, error) {
	return s.readURL(ctx, ref, ttl)
}

func TestPostSigningFailureHappensBeforeWrite(t *testing.T) {
	for _, update := range []bool{false, true} {
		t.Run(fmt.Sprint(update), func(t *testing.T) {
			repo := &signingPostRepo{assets: []MediaAsset{{ID: 1, Object: ObjectRef{Key: "one"}}, {ID: 2, Object: ObjectRef{Key: "two"}}}}
			want := errors.New("signing failed")
			calls := 0
			store := postSigningStorage{readURL: func(context.Context, ObjectRef, time.Duration) (string, error) {
				calls++
				if calls == 2 {
					return "", want
				}
				return "signed", nil
			}}
			uc := NewPostUsecase(repo, store, MediaPolicy{ReadTTL: time.Minute})
			input := PostInput{Title: "title", Content: "content", ImageIDs: []int64{1, 2}, ExpectedVersion: 1}
			var post *Post
			var err error
			if update {
				post, err = uc.Update(context.Background(), Actor{ID: 1, Role: RoleUser}, 1, input)
			} else {
				post, err = uc.Create(context.Background(), Actor{ID: 1, Role: RoleUser}, input)
			}
			require.ErrorIs(t, err, want)
			require.Nil(t, post)
			require.Zero(t, repo.writes)
		})
	}
}

func TestPostSigningAllowsNineImages(t *testing.T) {
	repo := &signingPostRepo{}
	input := PostInput{Title: "nine images", Content: "content"}
	for id := int64(1); id <= 9; id++ {
		repo.assets = append(repo.assets, MediaAsset{ID: id, Object: ObjectRef{Provider: "s3", Bucket: "private", Key: fmt.Sprint(id)}})
		input.ImageIDs = append(input.ImageIDs, id)
	}
	calls := 0
	store := postSigningStorage{readURL: func(_ context.Context, ref ObjectRef, _ time.Duration) (string, error) {
		require.Zero(t, repo.writes)
		calls++
		return "signed/" + ref.Key, nil
	}}
	post, err := NewPostUsecase(repo, store, MediaPolicy{ReadTTL: time.Minute}).Create(context.Background(), Actor{ID: 1, Role: RoleUser}, input)
	require.NoError(t, err)
	require.Len(t, post.Images, 9)
	require.Equal(t, 9, calls)
	for index, image := range post.Images {
		require.EqualValues(t, index+1, image.ID)
		require.Equal(t, "signed/"+fmt.Sprint(index+1), image.URL)
	}
}

func TestPostSigningOnlyBeforeCommitPreservesImageOrder(t *testing.T) {
	for _, update := range []bool{false, true} {
		t.Run(fmt.Sprint(update), func(t *testing.T) {
			repo := &signingPostRepo{assets: []MediaAsset{{ID: 1, Object: ObjectRef{Provider: "s3", Bucket: "private", Key: "one"}}, {ID: 2, Object: ObjectRef{Provider: "s3", Bucket: "private", Key: "two"}}}}
			calls := 0
			store := postSigningStorage{readURL: func(_ context.Context, ref ObjectRef, ttl time.Duration) (string, error) {
				require.Zero(t, repo.writes, "must not sign after commit")
				require.Equal(t, time.Minute, ttl)
				calls++
				return "signed/" + ref.Key, nil
			}}
			repo.beforeWrite = func(i PostInput) {
				require.Equal(t, 2, calls)
				require.Equal(t, []PostImageRef{{ID: 2, Object: repo.assets[1].Object}, {ID: 1, Object: repo.assets[0].Object}}, i.PreparedImages)
			}
			uc := NewPostUsecase(repo, store, MediaPolicy{ReadTTL: time.Minute})
			input := PostInput{Title: "title", Content: "content", ImageIDs: []int64{2, 1}, ExpectedVersion: 1}
			var post *Post
			var err error
			if update {
				post, err = uc.Update(context.Background(), Actor{ID: 1, Role: RoleUser}, 1, input)
			} else {
				post, err = uc.Create(context.Background(), Actor{ID: 1, Role: RoleUser}, input)
			}
			require.NoError(t, err)
			require.Equal(t, 1, repo.writes)
			require.Equal(t, 2, calls)
			require.EqualValues(t, 2, post.Images[0].ID)
			require.Equal(t, "signed/two", post.Images[0].URL)
			require.Equal(t, "signed/one", post.Images[1].URL)
		})
	}
}
