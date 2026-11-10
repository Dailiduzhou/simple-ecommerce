package data

import (
	"context"
	"fmt"
	"testing"

	userv1 "github.com/Dailiduzhou/simple-ecommerce/api/user/v1"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	mockdb "github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db/mock"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/golang/mock/gomock"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

func TestCreateUserMapsOnlyPhoneUniqueConflict(t *testing.T) {
	for _, tc := range []struct {
		name, code, constraint string
		conflict               bool
	}{
		{"phone conflict", "23505", "idx_users_phone_hash", true},
		{"other unique conflict", "23505", "users_pkey", false},
		{"other database error", "23503", "idx_users_phone_hash", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := mockdb.NewMockQuerier(gomock.NewController(t))
			cause := &pgconn.PgError{Code: tc.code, ConstraintName: tc.constraint}
			q.EXPECT().CreateUser(gomock.Any(), gomock.Any()).Return(db.User{}, fmt.Errorf("insert: %w", cause))
			_, err := NewUserRepo(&Data{q: q}, log.DefaultLogger).CreateUser(context.Background(), "name", "hash", "cipher", "password")
			if tc.conflict {
				require.True(t, userv1.IsUserAlreadyExists(err))
			} else {
				require.ErrorIs(t, err, cause)
				require.False(t, userv1.IsUserAlreadyExists(err))
			}
		})
	}
}
