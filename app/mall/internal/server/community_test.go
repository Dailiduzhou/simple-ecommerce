package server

import (
	"context"
	"errors"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	communityv1 "github.com/Dailiduzhou/simple-ecommerce/api/community/v1"
	mediav1 "github.com/Dailiduzhou/simple-ecommerce/api/media/v1"
	userv1 "github.com/Dailiduzhou/simple-ecommerce/api/user/v1"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/conf"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/service"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type communityAuth struct{ biz.AuthUsecase }

func (*communityAuth) IsTokenBlacklisted(ctx context.Context, id string) (bool, error) {
	if id == "unavailable" {
		return false, errors.New("Redis down")
	}
	return id == "revoked", nil
}

type communityLimiter struct{ calls atomic.Int32 }

func (l *communityLimiter) Allow(context.Context, int64, string, string) error {
	l.calls.Add(1)
	return nil
}

type transportHistory struct {
	biz.BrowsingHistoryRepo
	uid    atomic.Int64
	filter atomic.Pointer[biz.HistoryFilter]
}

func (r *transportHistory) Record(ctx context.Context, uid, id int64) (time.Time, error) {
	r.uid.Store(uid)
	return time.Now(), nil
}

func (r *transportHistory) List(ctx context.Context, uid int64, p biz.Page, f biz.HistoryFilter) ([]biz.BrowsingHistoryItem, string, error) {
	r.uid.Store(uid)
	r.filter.Store(&f)
	return []biz.BrowsingHistoryItem{{ProductID: 99, Name: "private", Available: true, LastViewedAt: time.Now()}}, "", nil
}

func (r *transportHistory) Delete(ctx context.Context, uid, id int64) error {
	r.uid.Store(uid)
	return nil
}
func (r *transportHistory) Clear(ctx context.Context, uid int64) error { r.uid.Store(uid); return nil }

type transportPosts struct{ biz.PostRepo }

func (*transportPosts) List(context.Context, int64, int64, biz.Page) ([]biz.Post, string, error) {
	return nil, "", nil
}

func (*transportPosts) Update(ctx context.Context, a biz.Actor, id int64, i biz.PostInput) (*biz.Post, error) {
	if e := a.Owns(1, false); e != nil {
		return nil, e
	}
	return nil, communityv1.ErrorPostVersionConflict("conflict")
}

func (*transportPosts) Delete(ctx context.Context, a biz.Actor, id int64) error {
	return a.Owns(1, true)
}

type transportComments struct{ biz.CommentRepo }

func (*transportComments) Delete(ctx context.Context, a biz.Actor, post, id int64) error {
	return a.Owns(1, true)
}

func communityToken(t *testing.T, uid int64, role, jti string) string {
	t.Helper()
	token, e := jwt.NewWithClaims(jwt.SigningMethodHS256, &biz.EcommerceClaims{UserID: uid, Role: role, RegisteredClaims: jwt.RegisteredClaims{ID: jti, ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))}}).SignedString([]byte(strings.Repeat("a", 32)))
	require.NoError(t, e)
	return token
}

func communityServices() (*service.UserService, *service.CommunityService, *service.MediaService, *transportHistory) {
	r := &transportHistory{}
	u := service.NewUserService(nil, nil, nil, biz.NewBrowsingHistoryUsecase(r), log.DefaultLogger)
	c := service.NewCommunityService(biz.NewPostUsecase(&transportPosts{}, nil, biz.MediaPolicy{}), biz.NewCommentUsecase(&transportComments{}))
	return u, c, service.NewMediaService(nil), r
}

func TestCommunityHTTPAuthenticationRoutesAndOwnership(t *testing.T) {
	user, community, media, history := communityServices()
	limiter := &communityLimiter{}
	srv := NewHTTPServer(&conf.Server{Http: &conf.Server_HTTP{}}, &conf.Auth{AccessTokenSecret: strings.Repeat("a", 32)}, &communityAuth{}, service.NewMallService(nil, nil, nil, nil, log.DefaultLogger), user, service.NewOrderService(nil), service.NewPaymentService(&callbackPaymentUsecase{}, nil, log.DefaultLogger), community, media, limiter, log.DefaultLogger)
	invoke := func(method, path, body, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		return w
	}
	for _, path := range []string{"/v1/users/me/browsing-history", "/v1/posts", "/v1/posts/1/comments", "/v1/posts/1/comments/1/replies", "/v1/media/images/1"} {
		require.Equal(t, 401, invoke("GET", path, "", "").Code, path)
	}
	for _, jti := range []string{"revoked", "unavailable"} {
		require.Equal(t, 401, invoke("GET", "/v1/users/me/browsing-history", "", communityToken(t, 1, "user", jti)).Code)
	}
	token := communityToken(t, 41, "admin", "live")
	for _, tc := range []struct{ method, path, body string }{{"POST", "/v1/users/me/browsing-history", `{"product_id":"99","user_id":"500"}`}, {"GET", "/v1/users/me/browsing-history?user_id=500", ""}, {"DELETE", "/v1/users/me/browsing-history/99", ""}, {"DELETE", "/v1/users/me/browsing-history", ""}} {
		w := invoke(tc.method, tc.path, tc.body, token)
		require.Equal(t, 200, w.Code, w.Body.String())
		require.EqualValues(t, 41, history.uid.Load())
	}
	body := `{"title":"x","content":"x","image_ids":["1"],"expected_version":"1"}`
	require.Equal(t, 403, invoke("PUT", "/v1/posts/1", body, communityToken(t, 2, "admin", "live")).Code)
	require.Equal(t, 409, invoke("PUT", "/v1/posts/1", body, communityToken(t, 1, "user", "live")).Code)
	require.Equal(t, 403, invoke("DELETE", "/v1/posts/1/comments/1", "", communityToken(t, 2, "user", "live")).Code)
	require.Equal(t, 200, invoke("DELETE", "/v1/posts/1/comments/1", "", communityToken(t, 2, "admin", "live")).Code)
	require.Equal(t, 403, invoke("DELETE", "/v1/posts/1", "", communityToken(t, 2, "user", "live")).Code)
	require.Equal(t, 200, invoke("DELETE", "/v1/posts/1", "", communityToken(t, 2, "admin", "live")).Code)
	require.Equal(t, 400, invoke("GET", "/v1/users/me/browsing-history?page_size=51", "", token).Code)
	// Day-window filters bind from query parameters and honour explicit offsets:
	// 2026-09-09T00:00:00+08:00 is 2026-09-08T16:00:00Z on the server clock.
	day := "start_time=" + url.QueryEscape("2026-09-09T00:00:00+08:00") + "&end_time=" + url.QueryEscape("2026-09-10T00:00:00+08:00")
	w := invoke("GET", "/v1/users/me/browsing-history?"+day, "", token)
	require.Equal(t, 200, w.Code, w.Body.String())
	got := history.filter.Load()
	require.NotNil(t, got)
	require.True(t, got.HasStart && got.HasEnd)
	require.True(t, got.Start.Equal(time.Date(2026, 9, 8, 16, 0, 0, 0, time.UTC)), "start instant: %s", got.Start)
	require.True(t, got.End.Equal(time.Date(2026, 9, 9, 16, 0, 0, 0, time.UTC)), "end instant: %s", got.End)
	// Reversed, degenerate and unparseable windows never reach the repository.
	reversed := "start_time=" + url.QueryEscape("2026-09-10T00:00:00Z") + "&end_time=" + url.QueryEscape("2026-09-09T00:00:00Z")
	require.Equal(t, 400, invoke("GET", "/v1/users/me/browsing-history?"+reversed, "", token).Code)
	equal := "start_time=" + url.QueryEscape("2026-09-09T00:00:00Z") + "&end_time=" + url.QueryEscape("2026-09-09T00:00:00Z")
	require.Equal(t, 400, invoke("GET", "/v1/users/me/browsing-history?"+equal, "", token).Code)
	require.Equal(t, 400, invoke("GET", "/v1/users/me/browsing-history?start_time=not-a-time", "", token).Code)
	require.Equal(t, 413, invoke("POST", "/v1/posts", strings.Repeat("x", 65<<10), token).Code)
	require.Positive(t, limiter.calls.Load())
}

// A missing limiter must not take down read-only endpoints; only rate-limited
// writes fail closed.
func TestReadsIgnoreMissingWriteLimiter(t *testing.T) {
	user, community, media, _ := communityServices()
	srv := NewHTTPServer(&conf.Server{Http: &conf.Server_HTTP{}}, &conf.Auth{AccessTokenSecret: strings.Repeat("a", 32)}, &communityAuth{}, service.NewMallService(nil, nil, nil, nil, log.DefaultLogger), user, service.NewOrderService(nil), service.NewPaymentService(&callbackPaymentUsecase{}, nil, log.DefaultLogger), community, media, nil, log.DefaultLogger)
	invoke := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+communityToken(t, 7, "user", "live"))
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		return w
	}
	require.Equal(t, 200, invoke("GET", "/v1/users/me/browsing-history", "").Code)
	require.Equal(t, 200, invoke("GET", "/v1/posts", "").Code)
	require.Equal(t, 503, invoke("POST", "/v1/posts", `{"title":"x","content":"x"}`).Code)
}

func TestCommunityGRPCAuthenticationAndErrorMappings(t *testing.T) {
	user, community, media, history := communityServices()
	srv := NewGRPCServer(&conf.Server{Grpc: &conf.Server_GRPC{Addr: "127.0.0.1:0"}}, &conf.Auth{AccessTokenSecret: strings.Repeat("a", 32)}, &communityAuth{}, service.NewMallService(nil, nil, nil, nil, log.DefaultLogger), user, service.NewOrderService(nil), service.NewPaymentService(&callbackPaymentUsecase{}, nil, log.DefaultLogger), community, media, &communityLimiter{}, log.DefaultLogger)
	endpoint, e := srv.Endpoint()
	require.NoError(t, e)
	done := make(chan error, 1)
	go func() { done <- srv.Start(context.Background()) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		require.NoError(t, srv.Stop(ctx))
		require.NoError(t, <-done)
	})
	conn, e := grpc.NewClient(endpoint.Host, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, e)
	defer conn.Close()
	client := userv1.NewUserClient(conn)
	pc := communityv1.NewCommunityClient(conn)
	mc := mediav1.NewMediaClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, e = client.ListBrowsingHistory(ctx, &userv1.ListBrowsingHistoryRequest{})
	require.Equal(t, codes.Unauthenticated, status.Code(e))
	_, e = pc.ListPosts(ctx, &communityv1.ListPostsRequest{})
	require.Equal(t, codes.Unauthenticated, status.Code(e))
	_, e = mc.GetImage(ctx, &mediav1.ImageRequest{Id: 1})
	require.Equal(t, codes.Unauthenticated, status.Code(e))
	auth := func(id int64, role, jti string) context.Context {
		return metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+communityToken(t, id, role, jti)))
	}
	_, e = client.ListBrowsingHistory(auth(12, "admin", "revoked"), &userv1.ListBrowsingHistoryRequest{})
	require.Equal(t, codes.Unauthenticated, status.Code(e))
	_, e = client.ListBrowsingHistory(auth(12, "admin", "unavailable"), &userv1.ListBrowsingHistoryRequest{})
	require.Equal(t, codes.Unauthenticated, status.Code(e))
	_, e = client.RecordProductView(auth(12, "admin", "live"), &userv1.RecordProductViewRequest{ProductId: 99})
	require.NoError(t, e)
	require.EqualValues(t, 12, history.uid.Load())
	_, e = client.ListBrowsingHistory(auth(12, "admin", "live"), &userv1.ListBrowsingHistoryRequest{PageSize: 51})
	require.Equal(t, codes.InvalidArgument, status.Code(e))
	_, e = client.ListBrowsingHistory(auth(12, "admin", "live"), &userv1.ListBrowsingHistoryRequest{StartTime: timestamppb.New(time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)), EndTime: timestamppb.New(time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC))})
	require.Equal(t, codes.InvalidArgument, status.Code(e))
	update := &communityv1.UpdatePostRequest{Id: 1, Title: "x", Content: "x", ImageIds: []int64{1}, ExpectedVersion: 1}
	_, e = pc.UpdatePost(auth(2, "admin", "live"), update)
	require.Equal(t, codes.PermissionDenied, status.Code(e))
	_, e = pc.UpdatePost(auth(1, "user", "live"), update)
	require.Equal(t, codes.Aborted, status.Code(e))
	_, e = pc.DeleteComment(auth(2, "user", "live"), &communityv1.DeleteCommentRequest{PostId: 1, Id: 1})
	require.Equal(t, codes.PermissionDenied, status.Code(e))
	_, e = pc.DeleteComment(auth(2, "admin", "live"), &communityv1.DeleteCommentRequest{PostId: 1, Id: 1})
	require.NoError(t, e)
}

func (*communityAuth) ValidateAccount(ctx context.Context, claims *biz.EcommerceClaims) error {
	return nil
}
