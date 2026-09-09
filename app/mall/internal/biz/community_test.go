package biz

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/png"
	"strings"
	"testing"
	"time"

	communityv1 "github.com/Dailiduzhou/simple-ecommerce/api/community/v1"
	"github.com/stretchr/testify/require"
)

func TestCommunityCursorScopeAndBounds(t *testing.T) {
	p, e := ParsePage("", Scope("comments", 1, 2), 0)
	require.NoError(t, e)
	require.EqualValues(t, 20, p.Limit)
	raw := NextCursor(p, time.Now().UTC().Truncate(time.Microsecond), 123)
	parsed, e := ParsePage(raw, p.Scope, 50)
	require.NoError(t, e)
	require.True(t, parsed.HasCursor)
	require.EqualValues(t, 123, parsed.ID)
	for _, bad := range []string{"%", strings.Repeat("a", 513), base64.RawURLEncoding.EncodeToString([]byte(`{"v":2}`)), raw + "="} {
		_, e = ParsePage(bad, p.Scope, 20)
		require.True(t, communityv1.IsInvalidCursor(e))
	}
	_, e = ParsePage(raw, Scope("comments", 2, 2), 20)
	require.Error(t, e)
	_, e = ParsePage(raw, p.Scope, 51)
	require.Error(t, e)
	_, e = ParsePage("", p.Scope, -1)
	require.Error(t, e)
}

func TestPostAndCommentUnicodeValidation(t *testing.T) {
	base := PostInput{Title: "  标题  ", Content: strings.Repeat("文", 5000), ImageIDs: []int64{1}}
	got, e := base.Validate()
	require.NoError(t, e)
	require.Equal(t, "标题", got.Title)
	for _, n := range []int{0, 1, 9, 10} {
		i := base
		i.ImageIDs = nil
		for k := 0; k < n; k++ {
			i.ImageIDs = append(i.ImageIDs, int64(k+1))
		}
		_, e := i.Validate()
		require.Equal(t, n == 10, e != nil)
	}
	for _, ids := range [][]int64{{1, 1}, {-1}, {0}} {
		i := base
		i.ImageIDs = ids
		_, e := i.Validate()
		require.Error(t, e)
	}
	for _, text := range []string{" ", "<script>hi</script>", "\x00", string([]byte{0xff}), strings.Repeat("字", 101)} {
		_, e := ValidText(text, 100)
		require.Error(t, e)
	}
	_, e = ValidText(strings.Repeat("字", 1000), 1000)
	require.NoError(t, e)
	_, e = ValidText(strings.Repeat("字", 1001), 1000)
	require.Error(t, e)
	require.NoError(t, (Actor{ID: 2, Admin: true}).Owns(1, true))
	require.Error(t, (Actor{ID: 2, Admin: true}).Owns(1, false))
	require.Error(t, (Actor{}).Validate())
}

func TestImageContentValidation(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 2, 3))))
	b := buf.Bytes()
	v, e := ValidateImage(b, "image/png", int64(len(b)))
	require.NoError(t, e)
	require.EqualValues(t, 2, v.Width)
	require.EqualValues(t, 3, v.Height)
	_, e = ValidateImage(b, "image/jpeg", int64(len(b)))
	require.Error(t, e)
	_, e = ValidateImage(b, "image/png", int64(len(b)+1))
	require.Error(t, e)
	_, e = ValidateImage(b[:30], "image/png", 30)
	require.Error(t, e)
	_, e = ValidateImage([]byte("<svg/>"), "image/svg+xml", 6)
	require.Error(t, e)
	buf.Reset()
	require.NoError(t, png.Encode(&buf, image.NewGray(image.Rect(0, 0, MaxImageDimension+1, 1))))
	_, e = ValidateImage(buf.Bytes(), "image/png", int64(buf.Len()))
	require.Error(t, e)
}

func TestUsecasesRejectUnauthenticatedBeforeRepositories(t *testing.T) {
	ctx := context.Background()
	_, e := NewPostUsecase(nil, nil, MediaPolicy{}).Create(ctx, Actor{}, PostInput{})
	require.Error(t, e)
	_, e = NewCommentUsecase(nil).Create(ctx, Actor{}, 1, "text", 0)
	require.Error(t, e)
	_, e = NewBrowsingHistoryUsecase(nil).Record(ctx, Actor{}, 1)
	require.Error(t, e)
	_, _, e = NewBrowsingHistoryUsecase(nil).List(ctx, Actor{}, "", 20, HistoryFilter{Start: time.Now(), End: time.Now(), HasStart: true, HasEnd: true})
	require.Error(t, e)
	_, _, e = NewMediaUsecase(nil, nil, MediaPolicy{}).CreateUpload(ctx, Actor{}, "image/png", 1)
	require.Error(t, e)
}
