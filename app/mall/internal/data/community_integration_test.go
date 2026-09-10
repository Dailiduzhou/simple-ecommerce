//go:build integration

package data

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	communityv1 "github.com/Dailiduzhou/simple-ecommerce/api/community/v1"
	mediav1 "github.com/Dailiduzhou/simple-ecommerce/api/media/v1"
	userv1 "github.com/Dailiduzhou/simple-ecommerce/api/user/v1"
	migrations "github.com/Dailiduzhou/simple-ecommerce/app/mall/db"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertype"
	"github.com/stretchr/testify/require"
)

type communityFixture struct {
	*correctnessFixture
	media             *MediaRepo
	posts             *PostRepo
	comments          *CommentRepo
	history           *BrowsingHistoryRepo
	actor, other      biz.Actor
	mediaIDs, postIDs []int64
}

func newCommunityFixture(t *testing.T) *communityFixture {
	f := newCorrectnessFixture(t)
	insert, e := NewRiverInsertClient(f.pool)
	require.NoError(t, e)
	policy, e := NewCommunityPolicy(nil)
	require.NoError(t, e)
	m := NewMediaRepo(f.data, f.tx, insert, policy)
	c := &communityFixture{correctnessFixture: f, media: m, posts: NewPostRepo(f.data, f.tx, m), comments: NewCommentRepo(f.data, f.tx), history: NewBrowsingHistoryRepo(f.data, f.tx, policy), actor: biz.Actor{ID: f.userID}}
	require.NoError(t, f.pool.QueryRow(f.ctx, `INSERT INTO users(nickname,phone_hash,phone_encrypt,password_hash) VALUES('other',$1,'secret','secret') RETURNING id`, f.prefix+"_other").Scan(&c.other.ID))
	t.Cleanup(func() {
		_, e := f.pool.Exec(f.ctx, `DELETE FROM river_job WHERE kind=$1 AND (args->>'media_id')::bigint=ANY($2::bigint[])`, biz.MediaDeleteKind, c.mediaIDs)
		require.NoError(t, e)
		_, e = f.pool.Exec(f.ctx, `DELETE FROM posts WHERE id=ANY($1::bigint[])`, c.postIDs)
		require.NoError(t, e)
		_, e = f.pool.Exec(f.ctx, `DELETE FROM media_assets WHERE id=ANY($1::bigint[])`, c.mediaIDs)
		require.NoError(t, e)
		_, e = f.pool.Exec(f.ctx, `DELETE FROM users WHERE id=$1`, c.other.ID)
		require.NoError(t, e)
	})
	return c
}

func (f *communityFixture) asset(t *testing.T, owner int64, status string) int64 {
	t.Helper()
	var id int64
	key := fmt.Sprintf("%s_%d", f.prefix, time.Now().UnixNano())
	require.NoError(t, f.pool.QueryRow(f.ctx, `INSERT INTO media_assets(owner_id,provider,bucket_name,object_key,staging_key,content_type,size_bytes,width,height,status,expires_at,upload_expires_at) VALUES($1,'s3','integration',$2,$3,'image/png',100,2,3,$4,clock_timestamp()+interval '1 day',clock_timestamp()-interval '2 minutes') RETURNING id`, owner, "images/"+key, "uploads/"+key, status).Scan(&id))
	f.mediaIDs = append(f.mediaIDs, id)
	return id
}

func (f *communityFixture) post(t *testing.T, a biz.Actor, ids ...int64) *biz.Post {
	t.Helper()
	if len(ids) == 0 {
		ids = []int64{f.asset(t, a.ID, "ready")}
	}
	p, e := f.posts.Create(f.ctx, a, biz.PostInput{Title: f.prefix, Content: "正文", ImageIDs: ids})
	require.NoError(t, e)
	f.postIDs = append(f.postIDs, p.ID)
	return p
}

func pageFor(t *testing.T, scope string, size int32) biz.Page {
	t.Helper()
	p, e := biz.ParsePage("", scope, size)
	require.NoError(t, e)
	return p
}

func sqlViolation(t *testing.T, e error) {
	t.Helper()
	var pg *pgconn.PgError
	require.ErrorAs(t, e, &pg)
	require.Contains(t, []string{"23514", "23503", "23505"}, pg.Code)
}

func TestCommunityIntegrationHistory(t *testing.T) {
	f := newCommunityFixture(t)
	ctx := f.ctx
	first, e := f.history.Record(ctx, f.actor.ID, f.productID)
	require.NoError(t, e)
	errs := make(chan error, 24)
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, e := f.history.Record(ctx, f.actor.ID, f.productID); errs <- e }()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		require.NoError(t, e)
	}
	var count int
	var original, last time.Time
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT count(*),min(first_viewed_at),max(last_viewed_at) FROM product_browsing_history WHERE user_id=$1`, f.actor.ID).Scan(&count, &original, &last))
	require.Equal(t, 1, count)
	require.Equal(t, first, original)
	require.False(t, last.Before(first))
	p := pageFor(t, biz.Scope("history", f.actor.ID), 1)
	rows, _, e := f.history.List(ctx, f.other.ID, p, biz.HistoryFilter{})
	require.NoError(t, e)
	require.Empty(t, rows)
	require.NoError(t, f.history.Delete(ctx, f.other.ID, f.productID))
	rows, _, e = f.history.List(ctx, f.actor.ID, p, biz.HistoryFilter{})
	require.NoError(t, e)
	require.Len(t, rows, 1)
	// Identical timestamps use product ID as the second key.
	var second int64
	require.NoError(t, f.pool.QueryRow(ctx, `INSERT INTO products(category_id,name,status) VALUES($1,'second',1) RETURNING id`, f.categoryID).Scan(&second))
	_, e = f.history.Record(ctx, f.actor.ID, second)
	require.NoError(t, e)
	_, e = f.pool.Exec(ctx, `UPDATE product_browsing_history SET last_viewed_at=clock_timestamp()+interval '1 second' WHERE user_id=$1`, f.actor.ID)
	require.NoError(t, e)
	// Give both records exactly the same value (clock_timestamp itself is volatile).
	_, e = f.pool.Exec(ctx, `UPDATE product_browsing_history SET last_viewed_at=$2 WHERE user_id=$1`, f.actor.ID, time.Now().Add(time.Second))
	require.NoError(t, e)
	rows, next, e := f.history.List(ctx, f.actor.ID, p, biz.HistoryFilter{})
	require.NoError(t, e)
	require.Equal(t, second, rows[0].ProductID)
	require.NotEmpty(t, next)
	p2, e := biz.ParsePage(next, p.Scope, 1)
	require.NoError(t, e)
	rows, _, e = f.history.List(ctx, f.actor.ID, p2, biz.HistoryFilter{})
	require.NoError(t, e)
	require.Equal(t, f.productID, rows[0].ProductID)
	_, e = f.pool.Exec(ctx, `UPDATE products SET status=0,deleted_at=clock_timestamp() WHERE id=$1`, f.productID)
	require.NoError(t, e)
	_, e = f.history.Record(ctx, f.actor.ID, f.productID)
	require.True(t, userv1.IsProductNotAvailable(e))
	rows, _, e = f.history.List(ctx, f.actor.ID, p2, biz.HistoryFilter{})
	require.NoError(t, e)
	require.False(t, rows[0].Available)
	require.Zero(t, rows[0].PriceMinor)
	_, e = f.history.Record(ctx, f.actor.ID, 9223372036854775807)
	require.True(t, userv1.IsProductNotAvailable(e))
	_, e = f.pool.Exec(ctx, `UPDATE product_browsing_history SET first_viewed_at=clock_timestamp()-interval '92 days',last_viewed_at=clock_timestamp()-interval '91 days' WHERE user_id=$1`, f.actor.ID)
	require.NoError(t, e)
	rows, _, e = f.history.List(ctx, f.actor.ID, p, biz.HistoryFilter{})
	require.NoError(t, e)
	require.Empty(t, rows)
	// A cleanup that encounters a locked stale tuple must not later delete its
	// refreshed version. SKIP LOCKED skips it, and a later run rechecks the cutoff.
	tx, e := f.pool.Begin(ctx)
	require.NoError(t, e)
	_, e = tx.Exec(ctx, `SELECT 1 FROM product_browsing_history WHERE user_id=$1 AND product_id=$2 FOR UPDATE`, f.actor.ID, second)
	require.NoError(t, e)
	_, e = f.history.Cleanup(ctx)
	require.NoError(t, e)
	_, e = tx.Exec(ctx, `UPDATE product_browsing_history SET last_viewed_at=clock_timestamp() WHERE user_id=$1 AND product_id=$2`, f.actor.ID, second)
	require.NoError(t, e)
	require.NoError(t, tx.Commit(ctx))
	_, e = f.history.Cleanup(ctx)
	require.NoError(t, e)
	rows, _, e = f.history.List(ctx, f.actor.ID, p, biz.HistoryFilter{})
	require.NoError(t, e)
	require.Len(t, rows, 1)
	// Clear and record share the user row lock; concurrent ordering may yield 0
	// or 1 row, never duplicates. An acknowledged later report always reappears.
	errs = make(chan error, 20)
	for i := 0; i < 20; i++ {
		go func(i int) {
			if i%2 == 0 {
				errs <- f.history.Clear(ctx, f.actor.ID)
			} else {
				_, e := f.history.Record(ctx, f.actor.ID, second)
				errs <- e
			}
		}(i)
	}
	for i := 0; i < 20; i++ {
		require.NoError(t, <-errs)
	}
	require.NoError(t, f.history.Clear(ctx, f.actor.ID))
	require.NoError(t, f.history.Clear(ctx, f.actor.ID))
	_, e = f.history.Record(ctx, f.actor.ID, second)
	require.NoError(t, e)
	rows, _, e = f.history.List(ctx, f.actor.ID, p, biz.HistoryFilter{})
	require.NoError(t, e)
	require.Len(t, rows, 1)
}

func browsingHistoryProductIDs(rows []biz.BrowsingHistoryItem) []int64 {
	ids := make([]int64, len(rows))
	for i, r := range rows {
		ids[i] = r.ProductID
	}
	return ids
}

func TestCommunityIntegrationHistoryTimeFilter(t *testing.T) {
	f := newCommunityFixture(t)
	ctx := f.ctx
	// Pin three footprints to distinct microsecond-aligned instants (PostgreSQL
	// stores microseconds) so window edges are deterministic; stamps[0] is oldest.
	base := time.Now().UTC().Truncate(time.Microsecond)
	stamps := []time.Time{base.Add(-3 * time.Hour), base.Add(-2 * time.Hour), base.Add(-time.Hour)}
	ids := make([]int64, len(stamps))
	for i := range ids {
		require.NoError(t, f.pool.QueryRow(ctx, `INSERT INTO products(category_id,name,status) VALUES($1,'history-filter',1) RETURNING id`, f.categoryID).Scan(&ids[i]))
		_, e := f.pool.Exec(ctx, `INSERT INTO product_browsing_history(user_id,product_id,first_viewed_at,last_viewed_at) VALUES($1,$2,$3,$3)`, f.actor.ID, ids[i], stamps[i])
		require.NoError(t, e)
	}
	scope := biz.Scope("history", f.actor.ID)
	// No filter: the newest-first default order is unchanged.
	rows, _, e := f.history.List(ctx, f.actor.ID, pageFor(t, scope, 20), biz.HistoryFilter{})
	require.NoError(t, e)
	require.Equal(t, []int64{ids[2], ids[1], ids[0]}, browsingHistoryProductIDs(rows))
	// Half-open [start, end): the start instant is included, the end excluded.
	window := biz.HistoryFilter{Start: stamps[0], End: stamps[2], HasStart: true, HasEnd: true}
	rows, _, e = f.history.List(ctx, f.actor.ID, pageFor(t, scope, 20), window)
	require.NoError(t, e)
	require.Equal(t, []int64{ids[1], ids[0]}, browsingHistoryProductIDs(rows))
	// A one-microsecond window isolates a single record, proving inclusive start.
	needle := biz.HistoryFilter{Start: stamps[1], End: stamps[1].Add(time.Microsecond), HasStart: true, HasEnd: true}
	rows, _, e = f.history.List(ctx, f.actor.ID, pageFor(t, scope, 20), needle)
	require.NoError(t, e)
	require.Equal(t, []int64{ids[1]}, browsingHistoryProductIDs(rows))
	// Single-sided windows keep the newest-first order.
	rows, _, e = f.history.List(ctx, f.actor.ID, pageFor(t, scope, 20), biz.HistoryFilter{Start: stamps[1], HasStart: true})
	require.NoError(t, e)
	require.Equal(t, []int64{ids[2], ids[1]}, browsingHistoryProductIDs(rows))
	rows, _, e = f.history.List(ctx, f.actor.ID, pageFor(t, scope, 20), biz.HistoryFilter{End: stamps[2], HasEnd: true})
	require.NoError(t, e)
	require.Equal(t, []int64{ids[1], ids[0]}, browsingHistoryProductIDs(rows))
	// A window with no overlap is an empty page, not an error, and emits no cursor.
	rows, next, e := f.history.List(ctx, f.actor.ID, pageFor(t, scope, 20), biz.HistoryFilter{Start: stamps[0].Add(-48 * time.Hour), End: stamps[0].Add(-24 * time.Hour), HasStart: true, HasEnd: true})
	require.NoError(t, e)
	require.Empty(t, rows)
	require.Empty(t, next)
	// Keyset pagination keeps working inside a window, one record per page.
	inWindow := biz.HistoryFilter{Start: stamps[0], End: stamps[2].Add(time.Microsecond), HasStart: true, HasEnd: true}
	var walked []int64
	p := pageFor(t, scope, 1)
	for i := 0; i < 3; i++ {
		rows, next, e = f.history.List(ctx, f.actor.ID, p, inWindow)
		require.NoError(t, e)
		require.Len(t, rows, 1)
		walked = append(walked, rows[0].ProductID)
		if next == "" {
			break
		}
		p, e = biz.ParsePage(next, scope, 1)
		require.NoError(t, e)
	}
	require.Equal(t, []int64{ids[2], ids[1], ids[0]}, walked)
	// A filter cannot resurrect data past the retention cutoff: a row inside
	// the requested window but older than the cutoff stays invisible.
	var staleID int64
	stale := time.Now().UTC().Add(-91 * 24 * time.Hour).Truncate(time.Microsecond)
	require.NoError(t, f.pool.QueryRow(ctx, `INSERT INTO products(category_id,name,status) VALUES($1,'history-stale',1) RETURNING id`, f.categoryID).Scan(&staleID))
	_, e = f.pool.Exec(ctx, `INSERT INTO product_browsing_history(user_id,product_id,first_viewed_at,last_viewed_at) VALUES($1,$2,$3,$3)`, f.actor.ID, staleID, stale)
	require.NoError(t, e)
	rows, _, e = f.history.List(ctx, f.actor.ID, pageFor(t, scope, 20), biz.HistoryFilter{Start: stale.Add(-24 * time.Hour), End: stale.Add(24 * time.Hour), HasStart: true, HasEnd: true})
	require.NoError(t, e)
	require.Empty(t, rows)
}

func TestCommunityIntegrationPostsLikesAndComments(t *testing.T) {
	f := newCommunityFixture(t)
	ctx := f.ctx
	m1 := f.asset(t, f.actor.ID, "ready")
	m2 := f.asset(t, f.actor.ID, "ready")
	post := f.post(t, f.actor, m1, m2)
	_, e := f.posts.Create(ctx, f.other, biz.PostInput{Title: "x", Content: "x", ImageIDs: []int64{m1}})
	require.True(t, mediav1.IsMediaForbidden(e))
	_, e = f.posts.Create(ctx, f.actor, biz.PostInput{Title: "x", Content: "x", ImageIDs: []int64{m1}})
	require.True(t, mediav1.IsMediaNotReady(e))
	pending := f.asset(t, f.actor.ID, "pending")
	_, e = f.posts.Create(ctx, f.actor, biz.PostInput{Title: "x", Content: "x", ImageIDs: []int64{pending}})
	require.True(t, mediav1.IsMediaNotReady(e))
	_, e = f.posts.Update(ctx, f.other, post.ID, biz.PostInput{Title: "x", Content: "x", ImageIDs: []int64{m1, m2}, ExpectedVersion: 1})
	require.True(t, communityv1.IsForbidden(e))
	admin := f.other
	admin.Admin = true
	_, e = f.posts.Update(ctx, admin, post.ID, biz.PostInput{Title: "x", Content: "x", ImageIDs: []int64{m1, m2}, ExpectedVersion: 1})
	require.True(t, communityv1.IsForbidden(e))
	updated, e := f.posts.Update(ctx, f.actor, post.ID, biz.PostInput{Title: "更新", Content: "正文", ImageIDs: []int64{m2, m1}, ExpectedVersion: 1})
	require.NoError(t, e)
	require.EqualValues(t, 2, updated.Version)
	require.Equal(t, m2, updated.Images[0].ID)
	_, e = f.posts.Update(ctx, f.actor, post.ID, biz.PostInput{Title: "旧", Content: "正文", ImageIDs: []int64{m1}, ExpectedVersion: 1})
	require.True(t, communityv1.IsPostVersionConflict(e))
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		go func() { errs <- f.posts.SetLike(ctx, f.other, post.ID, true) }()
	}
	for i := 0; i < 20; i++ {
		require.NoError(t, <-errs)
	}
	require.NoError(t, f.posts.SetLike(ctx, f.actor, post.ID, true))
	p, e := f.posts.Get(ctx, f.other.ID, post.ID)
	require.NoError(t, e)
	require.EqualValues(t, 2, p.LikeCount)
	require.True(t, p.LikedByMe)
	require.NoError(t, f.posts.SetLike(ctx, f.other, post.ID, false))
	require.NoError(t, f.posts.SetLike(ctx, f.other, post.ID, false))
	p, e = f.posts.Get(ctx, f.other.ID, post.ID)
	require.NoError(t, e)
	require.EqualValues(t, 1, p.LikeCount)
	require.False(t, p.LikedByMe)
	root, e := f.comments.Create(ctx, f.actor, post.ID, "root", 0)
	require.NoError(t, e)
	b, e := f.comments.Create(ctx, f.other, post.ID, "reply root", root.ID)
	require.NoError(t, e)
	c, e := f.comments.Create(ctx, f.other, post.ID, "reply reply", b.ID)
	require.NoError(t, e)
	require.Equal(t, root.ID, c.RootID)
	require.Equal(t, b.ID, c.ReplyToID)
	roots, _, e := f.comments.List(ctx, post.ID, 0, pageFor(t, "root", 20))
	require.NoError(t, e)
	require.Len(t, roots, 1)
	require.EqualValues(t, 2, roots[0].ReplyCount)
	replies, next, e := f.comments.List(ctx, post.ID, root.ID, pageFor(t, "reply", 1))
	require.NoError(t, e)
	require.Equal(t, b.ID, replies[0].ID)
	pg, e := biz.ParsePage(next, "reply", 1)
	require.NoError(t, e)
	replies, _, e = f.comments.List(ctx, post.ID, root.ID, pg)
	require.NoError(t, e)
	require.Equal(t, c.ID, replies[0].ID)
	require.True(t, communityv1.IsForbidden(f.comments.Delete(ctx, f.actor, post.ID, b.ID)), "post author does not own others' comments")
	require.NoError(t, f.comments.Delete(ctx, f.other, post.ID, b.ID))
	require.NoError(t, f.comments.Delete(ctx, f.other, post.ID, b.ID))
	replies, _, e = f.comments.List(ctx, post.ID, root.ID, pageFor(t, "reply", 20))
	require.NoError(t, e)
	require.Len(t, replies, 1)
	require.True(t, replies[0].ReplyToDeleted)
	require.Nil(t, replies[0].ReplyToAuthor)
	_, e = f.comments.Create(ctx, f.actor, post.ID, "bad", b.ID)
	require.True(t, communityv1.IsInvalidReplyTarget(e))
	require.NoError(t, f.comments.Delete(ctx, f.actor, post.ID, root.ID))
	roots, _, e = f.comments.List(ctx, post.ID, 0, pageFor(t, "root", 20))
	require.NoError(t, e)
	require.Len(t, roots, 1)
	require.True(t, roots[0].Deleted)
	require.Empty(t, roots[0].Content)
	require.Zero(t, roots[0].Author.ID)
	_, e = f.comments.Create(ctx, f.other, post.ID, "closed", c.ID)
	require.True(t, communityv1.IsCommentThreadClosed(e))
	p, e = f.posts.Get(ctx, f.actor.ID, post.ID)
	require.NoError(t, e)
	require.EqualValues(t, 1, p.CommentCount)
	require.NoError(t, f.comments.Delete(ctx, admin, post.ID, c.ID))
	roots, _, e = f.comments.List(ctx, post.ID, 0, pageFor(t, "root", 20))
	require.NoError(t, e)
	require.Empty(t, roots)
	require.NoError(t, f.posts.Delete(ctx, admin, post.ID))
	require.NoError(t, f.posts.Delete(ctx, admin, post.ID))
	_, e = f.posts.Get(ctx, f.actor.ID, post.ID)
	require.True(t, communityv1.IsPostNotFound(e))
	require.True(t, communityv1.IsPostNotFound(f.posts.SetLike(ctx, f.actor, post.ID, true)))
	_, e = f.comments.Create(ctx, f.actor, post.ID, "hidden", 0)
	require.True(t, communityv1.IsPostNotFound(e))
	var jobs int
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT count(*) FROM river_job WHERE kind=$1 AND (args->>'media_id')::bigint=ANY($2::bigint[])`, biz.MediaDeleteKind, []int64{m1, m2}).Scan(&jobs))
	require.Equal(t, 2, jobs)
}

func TestCommunityIntegrationCommentDatabaseConstraints(t *testing.T) {
	f := newCommunityFixture(t)
	ctx := f.ctx
	p := f.post(t, f.actor)
	p2 := f.post(t, f.other)
	a, e := f.comments.Create(ctx, f.actor, p.ID, "A", 0)
	require.NoError(t, e)
	b, e := f.comments.Create(ctx, f.actor, p.ID, "B", a.ID)
	require.NoError(t, e)
	other, e := f.comments.Create(ctx, f.actor, p.ID, "D", 0)
	require.NoError(t, e)
	_, e = f.comments.Create(ctx, f.other, p2.ID, "cross", a.ID)
	require.True(t, communityv1.IsInvalidReplyTarget(e))
	for _, args := range [][]any{{p2.ID, a.ID, a.ID}, {p.ID, b.ID, b.ID}, {p.ID, other.ID, b.ID}} {
		_, e = f.pool.Exec(ctx, `INSERT INTO post_comments(post_id,root_comment_id,reply_to_comment_id,content) VALUES($1,$2,$3,'illegal')`, args...)
		sqlViolation(t, e)
	}
	_, e = f.pool.Exec(ctx, `UPDATE post_comments SET root_comment_id=$1,reply_to_comment_id=$1 WHERE id=$2`, other.ID, b.ID)
	sqlViolation(t, e)
	_, e = f.pool.Exec(ctx, `UPDATE post_comments SET post_id=$1 WHERE id=$2`, p2.ID, a.ID)
	sqlViolation(t, e)
	_, e = f.pool.Exec(ctx, `UPDATE post_comments SET root_comment_id=id,reply_to_comment_id=id WHERE id=$1`, a.ID)
	sqlViolation(t, e)
}

func TestCommunityIntegrationDeleteRacesAndBinding(t *testing.T) {
	f := newCommunityFixture(t)
	ctx := f.ctx
	p := f.post(t, f.actor)
	root, e := f.comments.Create(ctx, f.actor, p.ID, "root", 0)
	require.NoError(t, e)
	start := make(chan struct{})
	errs := make(chan error, 3)
	go func() { <-start; errs <- f.comments.Delete(ctx, f.actor, p.ID, root.ID) }()
	go func() { <-start; _, e := f.comments.Create(ctx, f.other, p.ID, "concurrent", root.ID); errs <- e }()
	go func() { <-start; errs <- f.posts.SetLike(ctx, f.other, p.ID, true) }()
	close(start)
	for i := 0; i < 3; i++ {
		e := <-errs
		require.True(t, e == nil || communityv1.IsInvalidReplyTarget(e) || communityv1.IsCommentThreadClosed(e))
	}
	_, e = f.comments.Create(ctx, f.other, p.ID, "late", root.ID)
	require.Error(t, e)
	start = make(chan struct{})
	go func() { <-start; errs <- f.posts.Delete(ctx, f.actor, p.ID) }()
	go func() { <-start; errs <- f.posts.SetLike(ctx, f.other, p.ID, true) }()
	go func() { <-start; _, e := f.comments.Create(ctx, f.other, p.ID, "race", 0); errs <- e }()
	close(start)
	for i := 0; i < 3; i++ {
		e := <-errs
		require.True(t, e == nil || communityv1.IsPostNotFound(e))
	}
	require.True(t, communityv1.IsPostNotFound(f.posts.SetLike(ctx, f.other, p.ID, true)))
	// Cleanup state and binding are serialized by the media row lock. A resource
	// which cleanup has claimed can never be re-bound.
	id := f.asset(t, f.actor.ID, "ready")
	require.NoError(t, f.tx.InTx(ctx, func(ctx context.Context) error {
		rows, e := f.data.DB(ctx).LockMediaAssets(ctx, []int64{id})
		if e != nil {
			return e
		}
		return f.media.markDeleting(ctx, rows[0])
	}))
	_, e = f.posts.Create(ctx, f.actor, biz.PostInput{Title: "x", Content: "x", ImageIDs: []int64{id}})
	require.True(t, mediav1.IsMediaNotReady(e))
	// Verify I/O serialization with an in-flight verification, not a timing guess.
	pending := f.asset(t, f.actor.ID, "pending")
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, e := f.media.Complete(ctx, f.actor.ID, pending, func(context.Context, biz.MediaAsset) (biz.VerifiedImage, error) {
			close(entered)
			<-release
			return biz.VerifiedImage{ContentType: "image/png", Size: 100, Width: 2, Height: 3}, nil
		})
		done <- e
	}()
	<-entered
	require.NoError(t, f.tx.InTx(ctx, func(ctx context.Context) error {
		rows, e := f.data.DB(ctx).LockMediaAssets(ctx, []int64{pending})
		if e != nil {
			return e
		}
		return f.media.markDeleting(ctx, rows[0])
	}))
	removed := make(chan bool, 1)
	go func() {
		done <- f.media.Remove(ctx, pending, func(context.Context, biz.MediaAsset) error { removed <- true; return nil })
	}()
	select {
	case <-removed:
		t.Fatal("cleanup overtook verification")
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	for i := 0; i < 2; i++ {
		e := <-done
		require.True(t, e == nil || mediav1.IsMediaNotReady(e))
	}
	require.True(t, <-removed)
	require.NoError(t, f.media.Remove(ctx, pending, func(context.Context, biz.MediaAsset) error {
		t.Fatal("deleted object retried unnecessarily")
		return nil
	}))
}

func TestCommunityIntegrationUserDeletionAtomicity(t *testing.T) {
	f := newCommunityFixture(t)
	ctx := f.ctx
	post := f.post(t, f.actor)
	otherPost := f.post(t, f.other)
	root, e := f.comments.Create(ctx, f.actor, otherPost.ID, "departing", 0)
	require.NoError(t, e)
	reply, e := f.comments.Create(ctx, f.other, otherPost.ID, "survivor", root.ID)
	require.NoError(t, e)
	_, e = f.history.Record(ctx, f.actor.ID, f.productID)
	require.NoError(t, e)
	require.NoError(t, f.posts.SetLike(ctx, f.actor, otherPost.ID, true))
	orderID, _, _ := f.seedPayment(t, biz.PaymentStatusPending)
	repo := NewCommunityUserRepo(f.data, f.tx, f.media, log.DefaultLogger)
	require.NoError(t, f.rdb.Set(ctx, redisKey("user", f.actor.ID), "cached", time.Hour).Err())
	e = repo.DeleteUser(ctx, f.actor.ID)
	sqlViolation(t, e)
	_, e = f.posts.Get(ctx, f.other.ID, post.ID)
	require.NoError(t, e)
	c, e := f.data.DB(ctx).GetComment(ctx, db.GetCommentParams{PostID: otherPost.ID, ID: root.ID})
	require.NoError(t, e)
	require.False(t, c.DeletedAt.Valid)
	require.Equal(t, "departing", c.Content)
	var jobs int
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT count(*) FROM river_job WHERE kind=$1 AND (args->>'media_id')::bigint=ANY($2::bigint[])`, biz.MediaDeleteKind, f.mediaIDs).Scan(&jobs))
	require.Zero(t, jobs)
	require.Equal(t, "cached", f.rdb.Get(ctx, redisKey("user", f.actor.ID)).Val())
	_, e = f.pool.Exec(ctx, `DELETE FROM payments WHERE order_id=$1`, orderID)
	require.NoError(t, e)
	_, e = f.pool.Exec(ctx, `DELETE FROM orders WHERE id=$1`, orderID)
	require.NoError(t, e)
	require.NoError(t, repo.DeleteUser(ctx, f.actor.ID))
	require.Zero(t, f.rdb.Exists(ctx, redisKey("user", f.actor.ID)).Val())
	_, e = f.posts.Get(ctx, f.other.ID, post.ID)
	require.True(t, communityv1.IsPostNotFound(e))
	cs, _, e := f.comments.List(ctx, otherPost.ID, 0, pageFor(t, "roots", 20))
	require.NoError(t, e)
	require.Len(t, cs, 1)
	require.True(t, cs[0].Deleted)
	require.Empty(t, cs[0].Content)
	replies, _, e := f.comments.List(ctx, otherPost.ID, root.ID, pageFor(t, "replies", 20))
	require.NoError(t, e)
	require.Equal(t, reply.ID, replies[0].ID)
	require.True(t, replies[0].ReplyToDeleted)
	require.Nil(t, replies[0].ReplyToAuthor)
	p, e := f.posts.Get(ctx, f.other.ID, otherPost.ID)
	require.NoError(t, e)
	require.Zero(t, p.LikeCount)
	var n int
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT count(*) FROM product_browsing_history WHERE user_id=$1`, f.actor.ID).Scan(&n))
	require.Zero(t, n)
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT count(*) FROM river_job WHERE kind=$1 AND (args->>'media_id')::bigint=ANY($2::bigint[])`, biz.MediaDeleteKind, f.mediaIDs).Scan(&jobs))
	require.Equal(t, 1, jobs)
}

func TestCommunityIntegrationMediaRetryAndQueueRouting(t *testing.T) {
	f := newCommunityFixture(t)
	ctx := f.ctx
	id := f.asset(t, f.actor.ID, "ready")
	rollback := errors.New("rollback")
	require.ErrorIs(t, f.tx.InTx(ctx, func(ctx context.Context) error {
		m, e := f.data.DB(ctx).GetMediaAsset(ctx, id)
		if e != nil {
			return e
		}
		if e = f.media.markDeleting(ctx, m); e != nil {
			return e
		}
		return rollback
	}), rollback)
	m, e := f.data.DB(ctx).GetMediaAsset(ctx, id)
	require.NoError(t, e)
	require.Equal(t, "ready", m.Status)
	var n int
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT count(*) FROM river_job WHERE kind=$1 AND (args->>'media_id')::bigint=$2`, biz.MediaDeleteKind, id).Scan(&n))
	require.Zero(t, n)
	_, e = f.pool.Exec(ctx, `UPDATE media_assets SET expires_at=clock_timestamp()-interval '1 hour' WHERE id=$1`, id)
	require.NoError(t, e)
	_, e = f.media.Sweep(ctx)
	require.NoError(t, e)
	_, e = f.media.Sweep(ctx)
	require.NoError(t, e)
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT count(*) FROM river_job WHERE kind=$1 AND (args->>'media_id')::bigint=$2`, biz.MediaDeleteKind, id).Scan(&n))
	require.Equal(t, 1, n)
	require.Error(t, f.media.Remove(ctx, id, func(context.Context, biz.MediaAsset) error { return errors.New("storage down") }))
	m, e = f.data.DB(ctx).GetMediaAsset(ctx, id)
	require.NoError(t, e)
	require.Equal(t, "deleting", m.Status)
	require.NoError(t, f.media.Remove(ctx, id, func(context.Context, biz.MediaAsset) error { return nil }))
	m, e = f.data.DB(ctx).GetMediaAsset(ctx, id)
	require.NoError(t, e)
	require.Equal(t, "deleted", m.Status)
	var before, after int
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT count(*) FROM payment_reconciliation_failures`).Scan(&before))
	f.handler.HandleError(ctx, &rivertype.JobRow{ID: 123, Kind: biz.MediaDeleteKind, Attempt: 10, MaxAttempts: 10}, errors.New("do not leak signed URL"))
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT count(*) FROM payment_reconciliation_failures`).Scan(&after))
	require.Equal(t, before, after)
}

func TestCommunityIntegrationMigrationRoundTrip(t *testing.T) {
	dsn := os.Getenv(integrationPostgresEnv)
	if dsn == "" {
		t.Skip("dedicated integration database required")
	}
	cfg, e := pgxpool.ParseConfig(dsn)
	require.NoError(t, e)
	require.Contains(t, strings.ToLower(cfg.ConnConfig.Database), "integration")
	pool, e := pgxpool.NewWithConfig(context.Background(), cfg)
	require.NoError(t, e)
	defer pool.Close()
	// Isolated schema on a dedicated connection: never reset a shared database.
	conn, e := pool.Acquire(context.Background())
	require.NoError(t, e)
	defer conn.Release()
	schema := fmt.Sprintf("community_integration_%d", time.Now().UnixNano())
	ident := pgx.Identifier{schema}.Sanitize()
	_, e = conn.Exec(context.Background(), "CREATE SCHEMA "+ident)
	require.NoError(t, e)
	defer conn.Exec(context.Background(), "DROP SCHEMA "+ident+" CASCADE")
	_, e = conn.Exec(context.Background(), "SET search_path TO "+ident+",public")
	require.NoError(t, e)
	up, e := migrations.FS.ReadFile("migrations/000001_init_schema.up.sql")
	require.NoError(t, e)
	down, e := migrations.FS.ReadFile("migrations/000001_init_schema.down.sql")
	require.NoError(t, e)
	// pg_trgm is shared infrastructure, so do not drop its public extension from
	// a schema-local migration test. All business tables and triggers round-trip.
	down = []byte(strings.ReplaceAll(string(down), "DROP EXTENSION IF EXISTS pg_trgm;", ""))
	for _, s := range []string{string(up), string(down), string(up)} {
		_, e = conn.Exec(context.Background(), s)
		require.NoError(t, e)
	}
}

// Assert insertion uses the independent media queue even when the primary
// application's payment worker set doesn't register any community workers.
var _ river.JobArgs = biz.MediaDeleteArgs{}

func TestCommunityIntegrationMediaDeleteStateWriteFailure(t *testing.T) {
	f := newCommunityFixture(t)
	ctx := f.ctx
	id := f.asset(t, f.actor.ID, "deleting")
	// Fail only the DB acknowledgement after the external delete has succeeded.
	function := pgx.Identifier{f.prefix + "_fail_deleted"}.Sanitize()
	trigger := pgx.Identifier{f.prefix + "_fail_deleted"}.Sanitize()
	_, e := f.pool.Exec(ctx, fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.id=%d AND NEW.status='deleted' THEN RAISE EXCEPTION 'test acknowledgement failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER %s BEFORE UPDATE ON media_assets FOR EACH ROW EXECUTE FUNCTION %s()`, function, id, trigger, function))
	require.NoError(t, e)
	drop := func() {
		_, e := f.pool.Exec(ctx, "DROP TRIGGER IF EXISTS "+trigger+" ON media_assets; DROP FUNCTION IF EXISTS "+function+"()")
		require.NoError(t, e)
	}
	t.Cleanup(drop)
	removed := false
	calls := 0
	remove := func(context.Context, biz.MediaAsset) error { calls++; removed = true; return nil }
	require.Error(t, f.media.Remove(ctx, id, remove))
	require.True(t, removed)
	m, e := f.data.DB(ctx).GetMediaAsset(ctx, id)
	require.NoError(t, e)
	require.Equal(t, "deleting", m.Status)
	drop()
	require.NoError(t, f.media.Remove(ctx, id, remove))
	require.Equal(t, 2, calls)
	m, e = f.data.DB(ctx).GetMediaAsset(ctx, id)
	require.NoError(t, e)
	require.Equal(t, "deleted", m.Status)
}

type communityQueryCounter struct{ count atomic.Int32 }

func (c *communityQueryCounter) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	c.count.Add(1)
	return ctx
}
func (*communityQueryCounter) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}
func TestCommunityIntegrationPageQueriesAndExplain(t *testing.T) {
	f := newCommunityFixture(t)
	ctx := f.ctx
	rows, e := f.pool.Query(ctx, `INSERT INTO posts(author_id,title,content) SELECT $1,'explain','text' FROM generate_series(1,3000) RETURNING id`, f.actor.ID)
	require.NoError(t, e)
	var ids []int64
	for rows.Next() {
		var id int64
		require.NoError(t, rows.Scan(&id))
		ids = append(ids, id)
	}
	require.NoError(t, rows.Err())
	rows.Close()
	f.postIDs = append(f.postIDs, ids...)
	_, e = f.pool.Exec(ctx, `INSERT INTO post_likes(post_id,user_id) SELECT unnest($1::bigint[]),$2`, ids, f.other.ID)
	require.NoError(t, e)
	_, e = f.pool.Exec(ctx, `INSERT INTO post_comments(post_id,author_id,content) SELECT unnest($1::bigint[]),$2,'comment'`, ids, f.other.ID)
	require.NoError(t, e)
	_, e = f.pool.Exec(ctx, `WITH seeded AS (INSERT INTO products(category_id,name,status) SELECT $1,'history-explain',1 FROM generate_series(1,3000) RETURNING id) INSERT INTO product_browsing_history(user_id,product_id,first_viewed_at,last_viewed_at) SELECT $2,id,clock_timestamp()-interval '100 days',CASE WHEN id%30=0 THEN clock_timestamp()-interval '91 days' ELSE clock_timestamp() END FROM seeded`, f.categoryID, f.actor.ID)
	require.NoError(t, e)
	_, e = f.pool.Exec(ctx, `ANALYZE posts; ANALYZE post_likes; ANALYZE post_comments; ANALYZE product_browsing_history;`)
	require.NoError(t, e)
	counter := &communityQueryCounter{}
	cfg, e := pgxpool.ParseConfig(os.Getenv(integrationPostgresEnv))
	require.NoError(t, e)
	cfg.ConnConfig.Tracer = counter
	pool, e := pgxpool.NewWithConfig(ctx, cfg)
	require.NoError(t, e)
	defer pool.Close()
	repo := NewPostRepo(&Data{pool: pool, q: db.New(pool)}, f.tx, f.media)
	for _, size := range []int32{1, 20, 50} {
		counter.count.Store(0)
		posts, _, e := repo.List(ctx, f.other.ID, f.actor.ID, pageFor(t, "posts", size))
		require.NoError(t, e)
		require.Len(t, posts, int(size))
		require.EqualValues(t, 5, counter.count.Load(), "BEGIN/COMMIT plus list, statistics and images; no per-post queries")
		require.EqualValues(t, 1, posts[0].LikeCount)
		require.EqualValues(t, 1, posts[0].CommentCount)
	}
	queries := []string{
		`SELECT p.id FROM posts p WHERE p.deleted_at IS NULL ORDER BY p.created_at DESC,p.id DESC LIMIT 20`,
		fmt.Sprintf(`SELECT p.id,(SELECT count(*) FROM post_likes l WHERE l.post_id=p.id),(SELECT count(*) FROM post_comments c WHERE c.post_id=p.id AND c.deleted_at IS NULL) FROM posts p WHERE p.id=ANY(ARRAY[%d,%d]::bigint[])`, ids[0], ids[1]),
		fmt.Sprintf(`SELECT c.id FROM post_comments c WHERE c.post_id=%d AND c.root_comment_id IS NULL AND (c.deleted_at IS NULL OR EXISTS(SELECT 1 FROM post_comments r WHERE r.post_id=c.post_id AND r.root_comment_id=c.id AND r.deleted_at IS NULL)) ORDER BY c.created_at DESC,c.id DESC LIMIT 20`, ids[0]),
		`SELECT user_id,product_id FROM product_browsing_history WHERE last_viewed_at<=CURRENT_TIMESTAMP-interval '90 days' ORDER BY last_viewed_at,user_id,product_id LIMIT 100 FOR UPDATE SKIP LOCKED`,
	}
	for _, query := range queries {
		rows, e := f.pool.Query(ctx, "EXPLAIN (ANALYZE, BUFFERS) "+query)
		require.NoError(t, e)
		var plan []string
		for rows.Next() {
			var line string
			require.NoError(t, rows.Scan(&line))
			plan = append(plan, line)
		}
		require.NoError(t, rows.Err())
		rows.Close()
		text := strings.Join(plan, "\n")
		require.Contains(t, text, "Execution Time")
		require.Contains(t, text, "Index", "representative bounded lookup uses an index")
		t.Log(text)
	}
}

type communityDeleteTestWorker struct {
	river.WorkerDefaults[biz.MediaDeleteArgs]
	repo     *MediaRepo
	attempts atomic.Int32
}

func (w *communityDeleteTestWorker) Work(ctx context.Context, j *river.Job[biz.MediaDeleteArgs]) error {
	return w.repo.Remove(ctx, j.Args.MediaID, func(context.Context, biz.MediaAsset) error {
		if w.attempts.Add(1) == 1 {
			return errors.New("transient storage error")
		}
		return nil
	})
}

type communityImmediateRetry struct{}

func (communityImmediateRetry) NextRetry(*rivertype.JobRow) time.Time { return time.Now() }
func TestCommunityIntegrationRealRiverRetriesMediaJob(t *testing.T) {
	f := newCommunityFixture(t)
	ctx, cancel := context.WithTimeout(f.ctx, 15*time.Second)
	defer cancel()
	id := f.asset(t, f.actor.ID, "deleting")
	worker := &communityDeleteTestWorker{repo: f.media}
	workers := river.NewWorkers()
	river.AddWorker(workers, worker)
	client, e := river.NewClient(riverpgxv5.New(f.pool), &river.Config{Queues: map[string]river.QueueConfig{"media": {MaxWorkers: 1}}, Workers: workers, RetryPolicy: communityImmediateRetry{}, FetchCooldown: 10 * time.Millisecond, FetchPollInterval: 20 * time.Millisecond, JobTimeout: 5 * time.Second})
	require.NoError(t, e)
	events, unsubscribe := client.Subscribe(river.EventKindJobCompleted)
	defer unsubscribe()
	inserted, e := client.Insert(ctx, biz.MediaDeleteArgs{MediaID: id}, &river.InsertOpts{Queue: "media", MaxAttempts: 3})
	require.NoError(t, e)
	require.NoError(t, client.Start(ctx))
	defer client.Stop(context.Background())
	select {
	case event := <-events:
		require.Equal(t, inserted.Job.ID, event.Job.ID)
		require.Equal(t, 2, event.Job.Attempt)
	case <-ctx.Done():
		t.Fatal("media job did not recover from the transient failure")
	}
	require.EqualValues(t, 2, worker.attempts.Load())
	m, e := f.data.DB(ctx).GetMediaAsset(ctx, id)
	require.NoError(t, e)
	require.Equal(t, "deleted", m.Status)
	require.NoError(t, client.Stop(ctx))
}
