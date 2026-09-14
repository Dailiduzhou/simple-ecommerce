package biz

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type textOnlyPostRepo struct {
	PostRepo
	input PostInput
}

func (r *textOnlyPostRepo) Create(_ context.Context, a Actor, i PostInput) (*Post, error) {
	r.input = i
	return &Post{ID: 1, Author: Author{ID: a.ID}, Title: i.Title, Content: i.Content, Version: 1}, nil
}

func (r *textOnlyPostRepo) Update(_ context.Context, a Actor, id int64, i PostInput) (*Post, error) {
	r.input = i
	return &Post{ID: id, Author: Author{ID: a.ID}, Title: i.Title, Content: i.Content, Version: i.ExpectedVersion + 1}, nil
}

func TestPostUsecaseOptionalImages(t *testing.T) {
	for _, tc := range []struct {
		name string
		ids  []int64
	}{{"omitted", nil}, {"empty", []int64{}}} {
		t.Run(tc.name, func(t *testing.T) {
			r := &textOnlyPostRepo{}
			// A text-only create/update must not call object storage.
			u := NewPostUsecase(r, nil, MediaPolicy{})
			input := PostInput{Title: " 标题 ", Content: " 正文 ", ImageIDs: tc.ids}
			p, err := u.Create(context.Background(), Actor{ID: 1}, input)
			require.NoError(t, err)
			require.Empty(t, p.Images)
			require.Equal(t, tc.ids, r.input.ImageIDs)
			require.Equal(t, "标题", r.input.Title)

			input.ExpectedVersion = p.Version
			p, err = u.Update(context.Background(), Actor{ID: 1}, p.ID, input)
			require.NoError(t, err)
			require.Empty(t, p.Images)
			require.Equal(t, tc.ids, r.input.ImageIDs)
			require.EqualValues(t, 2, p.Version)
		})
	}
}
