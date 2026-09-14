package biz

import (
	"context"
	"strings"
	"testing"

	"buf.build/go/protovalidate"
	userv1 "github.com/Dailiduzhou/simple-ecommerce/api/user/v1"
	"github.com/Dailiduzhou/simple-ecommerce/pkg/pwdhash"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestReviewPasswordBounds(t *testing.T) {
	for _, n := range []int{0, 7, 8, 72, 73} {
		t.Run(strings.Repeat("x", n), func(t *testing.T) {
			password := strings.Repeat("x", n)
			valid := n >= 8 && n <= 72
			for _, msg := range []proto.Message{&userv1.RegisterRequest{Phone: "13800138000", Password: password}, &userv1.LoginRequest{Phone: "13800138000", Password: password}} {
				e := protovalidate.Validate(msg)
				if valid {
					require.NoError(t, e)
				} else {
					require.Error(t, e)
				}
			}
			repo := &fakeUserRepo{}
			uc := NewUserUsecase(repo, &fakeAuthRepo{}, testUserAuth(), log.DefaultLogger)
			if !valid {
				_, e := uc.Register(context.Background(), "13800138000", password)
				require.Error(t, e)
				_, e = uc.Login(context.Background(), "13800138000", password)
				require.Error(t, e)
				return
			}
			hash, e := pwdhash.HashPassword(password)
			require.NoError(t, e)
			repo.getUserByPhoneHash = func(context.Context, string) (*User, error) { return &User{ID: 1, PasswordHash: hash}, nil }
			_, e = uc.Login(context.Background(), "13800138000", password)
			require.NoError(t, e)
		})
	}
}
