//go:build integration

package data

import (
	"testing"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/stretchr/testify/require"
)

func TestCommunityIntegrationOptionalPostImages(t *testing.T) {
	f := newCommunityFixture(t)
	// Do not use f.post: that helper supplies an image when IDs are omitted.
	p, err := f.posts.Create(f.ctx, f.actor, biz.PostInput{Title: "纯文本", Content: "正文"})
	require.NoError(t, err)
	f.postIDs = append(f.postIDs, p.ID)
	require.Empty(t, p.Images)

	got, err := f.posts.Get(f.ctx, f.actor.ID, p.ID)
	require.NoError(t, err)
	require.Empty(t, got.Images)
	posts, _, err := f.posts.List(f.ctx, f.actor.ID, f.actor.ID, pageFor(t, biz.Scope("posts", f.actor.ID), 20))
	require.NoError(t, err)
	require.Len(t, posts, 1)
	require.Equal(t, p.ID, posts[0].ID)
	require.Empty(t, posts[0].Images)

	for _, tc := range []struct {
		name string
		ids  []int64
	}{{"omitted", nil}, {"empty", []int64{}}} {
		t.Run(tc.name, func(t *testing.T) {
			id := f.asset(t, f.actor.ID, "ready")
			p, err = f.posts.Update(f.ctx, f.actor, p.ID, biz.PostInput{
				Title: "有图片", Content: "正文", ImageIDs: []int64{id}, ExpectedVersion: p.Version,
			})
			require.NoError(t, err)
			require.Len(t, p.Images, 1)
			version := p.Version
			p, err = f.posts.Update(f.ctx, f.actor, p.ID, biz.PostInput{
				Title: "移除全部图片", Content: "正文", ImageIDs: tc.ids, ExpectedVersion: version,
			})
			require.NoError(t, err)
			require.Empty(t, p.Images)
			require.Equal(t, version+1, p.Version)
			var bindings, jobs int
			var status string
			require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT count(*) FROM post_images WHERE post_id=$1`, p.ID).Scan(&bindings))
			require.Zero(t, bindings)
			require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT status FROM media_assets WHERE id=$1`, id).Scan(&status))
			require.Equal(t, "deleting", status)
			require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT count(*) FROM river_job WHERE kind=$1 AND (args->>'media_id')::bigint=$2`, biz.MediaDeleteKind, id).Scan(&jobs))
			require.Equal(t, 1, jobs)
		})
	}
}
