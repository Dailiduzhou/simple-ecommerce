package data

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	stderrors "errors"
	"time"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

var (
	_ biz.UserRepo            = (*UserRepo)(nil)
	_ biz.ShippingAddressRepo = (*ShippingAddressRepo)(nil)
)

type UserRepo struct {
	data *Data
	log  *log.Helper
}

func NewUserRepo(data *Data, logger log.Logger) *UserRepo {
	return &UserRepo{data: data, log: log.NewHelper(logger)}
}

type ShippingAddressRepo struct {
	data *Data
	tx   biz.TxManager
	log  *log.Helper
}

func NewShippingAddressRepo(data *Data, tx biz.TxManager, logger log.Logger) *ShippingAddressRepo {
	return &ShippingAddressRepo{data: data, tx: tx, log: log.NewHelper(logger)}
}

func (r *UserRepo) CreateUser(ctx context.Context, nickname, phoneHash, phoneEncrypt, passwordHash string) (*biz.User, error) {
	if nickname == "" {
		b := make([]byte, 4)
		rand.Read(b)
		nickname = "u" + hex.EncodeToString(b)
	}
	u, err := r.data.DB(ctx).CreateUser(ctx, db.CreateUserParams{
		Nickname:     nickname,
		PhoneHash:    phoneHash,
		PhoneEncrypt: phoneEncrypt,
		PasswordHash: passwordHash,
		Role:         "user",
	})
	if err != nil {
		return nil, err
	}
	bizUser := toBizUser(u)
	bumpCacheGeneration(ctx, r.data.rdb, r.log, userGenerationKey(bizUser.ID))
	return bizUser, nil
}

func (r *UserRepo) GetAuthUser(ctx context.Context, id int64) (*biz.User, error) {
	row, err := r.data.DB(ctx).GetUserByID(ctx, id)
	if stderrors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return toBizUser(row), nil
}

func (r *UserRepo) GetUserByID(ctx context.Context, id int64) (*biz.User, error) {
	key := generatedEntityCacheKey(ctx, r.data, r.log, userGenerationKey(id), redisKey("user", id))
	return cacheAside(ctx, r.data, r.log, key, r.getCache, r.setCache, func() (*biz.User, error) {
		row, err := r.data.DB(ctx).GetUserByID(ctx, id)
		if stderrors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		return toBizUser(row), nil
	})
}

// GetUserByPhoneHash is an authentication lookup. Never serve credentials or
// account-existence decisions from the public-profile cache.
func (r *UserRepo) GetUserByPhoneHash(ctx context.Context, phoneHash string) (*biz.User, error) {
	u, err := r.data.DB(ctx).GetUserByPhoneHash(ctx, phoneHash)
	if stderrors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	user := toBizUser(u)
	if !inTransaction(ctx) {
		if key := generatedEntityCacheKey(ctx, r.data, r.log, userGenerationKey(user.ID), redisKey("user", user.ID)); key != "" {
			r.setCache(ctx, key, user)
		}
	}
	return user, nil
}

func (r *UserRepo) UpdateUser(ctx context.Context, id int64, nickname, realName string) (*biz.User, error) {
	u, err := r.data.DB(ctx).UpdateUser(ctx, db.UpdateUserParams{
		ID:       id,
		Nickname: nickname,
		RealName: realName,
	})
	if err != nil {
		return nil, err
	}
	bizUser := toBizUser(u)
	bumpCacheGeneration(ctx, r.data.rdb, r.log, userGenerationKey(id))
	return bizUser, nil
}

func (r *UserRepo) DeleteUser(ctx context.Context, id int64) error {
	err := r.data.DB(ctx).DeleteUser(ctx, id)
	if err != nil {
		return err
	}
	bumpCacheGeneration(ctx, r.data.rdb, r.log, userGenerationKey(id))
	return nil
}

func (r *UserRepo) getCache(ctx context.Context, key string) (*biz.User, error) {
	return readJSONCache[*biz.User](ctx, r.data, key)
}

func (r *UserRepo) setCache(ctx context.Context, key string, user *biz.User) {
	if user == nil {
		writeJSONCache(ctx, r.data, r.log, key, user, negativeCacheTTL)
		return
	}
	profile := struct {
		ID        int64
		Nickname  string
		RealName  string
		Role      string
		CreatedAt time.Time
		UpdatedAt time.Time
	}{user.ID, user.Nickname, user.RealName, user.Role, user.CreatedAt, user.UpdatedAt}
	writeJSONCache(ctx, r.data, r.log, key, profile, cacheTTL())
}

func (r *UserRepo) deleteCache(ctx context.Context, key string) {
	deleteJSONCache(ctx, r.data, r.log, key)
}

func userGenerationKey(id int64) string {
	return redisKey("user", id, "gen")
}

func toBizUser(u db.User) *biz.User {
	return &biz.User{
		ID:           u.ID,
		Nickname:     u.Nickname,
		RealName:     u.RealName,
		PhoneHash:    u.PhoneHash,
		PhoneEncrypt: u.PhoneEncrypt,
		PasswordHash: u.PasswordHash,
		Role:         u.Role,
		CreatedAt:    u.CreatedAt.Time,
		UpdatedAt:    u.UpdatedAt.Time,
	}
}

func toBizShippingAddress(sa db.ShippingAddress) biz.ShippingAddress {
	return biz.ShippingAddress{
		ID:                   sa.ID,
		UserID:               sa.UserID,
		ReceiverName:         sa.ReceiverName,
		ReceiverPhoneHash:    sa.ReceiverPhoneHash,
		ReceiverPhoneEncrypt: sa.ReceiverPhoneEncrypt,
		Province:             sa.Province,
		City:                 sa.City,
		District:             sa.District,
		DetailAddress:        sa.DetailAddress,
		AddressTag:           sa.AddressTag.String,
		IsDefault:            sa.IsDefault,
		CreatedAt:            sa.CreatedAt.Time,
		UpdatedAt:            sa.UpdatedAt.Time,
	}
}

func (r *ShippingAddressRepo) getCache(ctx context.Context, key string) (*biz.ShippingAddress, error) {
	return readJSONCache[*biz.ShippingAddress](ctx, r.data, key)
}

func (r *ShippingAddressRepo) getListCache(ctx context.Context, key string) ([]biz.ShippingAddress, error) {
	return readJSONCache[[]biz.ShippingAddress](ctx, r.data, key)
}

func (r *ShippingAddressRepo) setCache(ctx context.Context, key string, value *biz.ShippingAddress) {
	writeJSONCache(ctx, r.data, r.log, key, value, cacheTTL())
}

func (r *ShippingAddressRepo) setListCache(ctx context.Context, key string, value []biz.ShippingAddress) {
	writeJSONCache(ctx, r.data, r.log, key, value, cacheTTL())
}

func (r *ShippingAddressRepo) deleteCache(ctx context.Context, key string) {
	deleteJSONCache(ctx, r.data, r.log, key)
}

func shippingAddressGenerationKey(userID, addressID int64) string {
	return redisKey("shipping_addr", "user", userID, addressID, "gen")
}

func shippingAddressListGenerationKey(userID int64) string {
	return redisKey("shipping_addr", "user", userID, "list:gen")
}

func (r *ShippingAddressRepo) CreateShippingAddress(ctx context.Context, userID int64, receiverName string, receiverPhoneHash string, receiverPhoneEncrypt string, province string, city string, district string, detailAddress string, addressTag string, isDefault bool) (*biz.ShippingAddress, error) {
	isValid := addressTag != ""

	var sd db.ShippingAddress
	var oldDefaultID int64
	err := r.tx.InTx(ctx, func(ctx context.Context) error {
		q := querierFromContext(ctx, nil)
		if isDefault {
			if old, err := q.GetDefaultShippingAddress(ctx, userID); err == nil {
				oldDefaultID = old.ID
			} else if !stderrors.Is(err, pgx.ErrNoRows) {
				return err
			}
			if err := q.ClearDefaultShippingAddress(ctx, userID); err != nil {
				return err
			}
		}
		var err error
		sd, err = q.CreateShippingAddress(ctx, db.CreateShippingAddressParams{UserID: userID, ReceiverName: receiverName, ReceiverPhoneHash: receiverPhoneHash, ReceiverPhoneEncrypt: receiverPhoneEncrypt, Province: province, City: city, District: district, DetailAddress: detailAddress, AddressTag: pgtype.Text{String: addressTag, Valid: isValid}, IsDefault: isDefault})
		return err
	})
	if err != nil {
		return nil, err
	}
	result := toBizShippingAddress(sd)
	bumpCacheGeneration(ctx, r.data.rdb, r.log, shippingAddressGenerationKey(userID, result.ID))
	bumpCacheGeneration(ctx, r.data.rdb, r.log, shippingAddressListGenerationKey(userID))
	if oldDefaultID > 0 {
		bumpCacheGeneration(ctx, r.data.rdb, r.log, shippingAddressGenerationKey(userID, oldDefaultID))
	}
	return &result, nil
}

func (r *ShippingAddressRepo) ListShippingAddressesByUser(ctx context.Context, userID int64) ([]biz.ShippingAddress, error) {
	key := generatedEntityCacheKey(ctx, r.data, r.log, shippingAddressListGenerationKey(userID), redisKey("shipping_addr", "user", userID))
	return cacheAside(ctx, r.data, r.log, key, r.getListCache, r.setListCache, func() ([]biz.ShippingAddress, error) {
		rows, err := r.data.DB(ctx).ListShippingAddressesByUser(ctx, userID)
		if err != nil {
			return nil, err
		}
		addresses := make([]biz.ShippingAddress, len(rows))
		for i, row := range rows {
			addresses[i] = toBizShippingAddress(row)
		}
		return addresses, nil
	})
}

func (r *ShippingAddressRepo) GetShippingAddress(ctx context.Context, id int64, userID int64) (*biz.ShippingAddress, error) {
	key := generatedEntityCacheKey(ctx, r.data, r.log, shippingAddressGenerationKey(userID, id), shippingAddressCacheKey(userID, id))
	address, err := cacheAside(ctx, r.data, r.log, key, r.getCache, r.setCache, func() (*biz.ShippingAddress, error) {
		row, err := r.data.DB(ctx).GetShippingAddress(ctx, db.GetShippingAddressParams{ID: id, UserID: userID})
		if stderrors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		value := toBizShippingAddress(row)
		return &value, nil
	})
	if err != nil {
		return nil, err
	}
	if address == nil {
		return nil, biz.ErrShippingAddressNotFound
	}
	if address.UserID != userID || address.ID != id {
		r.deleteCache(ctx, key)
		return nil, biz.ErrShippingAddressNotFound
	}
	return address, nil
}

func (r *ShippingAddressRepo) UpdateShippingAddress(ctx context.Context, id int64, userID int64, receiverName string, receiverPhoneHash string, receiverPhoneEncrypt string, province string, city string, district string, detailAddress string, addressTag string) (*biz.ShippingAddress, error) {
	isValid := addressTag != ""

	sa, err := querierFromContext(ctx, r.data.q).UpdateShippingAddress(ctx, db.UpdateShippingAddressParams{
		ID:                   id,
		UserID:               userID,
		ReceiverName:         receiverName,
		ReceiverPhoneHash:    receiverPhoneHash,
		ReceiverPhoneEncrypt: receiverPhoneEncrypt,
		Province:             province,
		City:                 city,
		District:             district,
		DetailAddress:        detailAddress,
		AddressTag:           pgtype.Text{String: addressTag, Valid: isValid},
	})
	if err != nil {
		return nil, err
	}
	result := toBizShippingAddress(sa)
	bumpCacheGeneration(ctx, r.data.rdb, r.log, shippingAddressGenerationKey(userID, id))
	bumpCacheGeneration(ctx, r.data.rdb, r.log, shippingAddressListGenerationKey(userID))
	return &result, nil
}

func (r *ShippingAddressRepo) SetDefaultShippingAddress(ctx context.Context, id int64, userID int64) error {
	var oldDefaultID int64
	err := r.tx.InTx(ctx, func(ctx context.Context) error {
		q := querierFromContext(ctx, nil)
		if _, err := q.GetShippingAddress(ctx, db.GetShippingAddressParams{ID: id, UserID: userID}); err != nil {
			if stderrors.Is(err, pgx.ErrNoRows) {
				return biz.ErrShippingAddressNotFound
			}
			return err
		}
		if old, err := q.GetDefaultShippingAddress(ctx, userID); err == nil {
			oldDefaultID = old.ID
		} else if !stderrors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err := q.ClearDefaultShippingAddress(ctx, userID); err != nil {
			return err
		}
		return q.SetDefaultShippingAddress(ctx, db.SetDefaultShippingAddressParams{ID: id, UserID: userID})
	})
	if err != nil {
		return err
	}
	bumpCacheGeneration(ctx, r.data.rdb, r.log, shippingAddressGenerationKey(userID, id))
	if oldDefaultID > 0 {
		bumpCacheGeneration(ctx, r.data.rdb, r.log, shippingAddressGenerationKey(userID, oldDefaultID))
	}
	bumpCacheGeneration(ctx, r.data.rdb, r.log, shippingAddressListGenerationKey(userID))
	return nil
}

func (r *ShippingAddressRepo) DeleteShippingAddress(ctx context.Context, id int64, userID int64) error {
	err := querierFromContext(ctx, r.data.q).DeleteShippingAddress(ctx, db.DeleteShippingAddressParams{
		ID:     id,
		UserID: userID,
	})
	if err != nil {
		return err
	}
	bumpCacheGeneration(ctx, r.data.rdb, r.log, shippingAddressGenerationKey(userID, id))
	bumpCacheGeneration(ctx, r.data.rdb, r.log, shippingAddressListGenerationKey(userID))
	return nil
}

func shippingAddressCacheKey(userID, addressID int64) string {
	return redisKey("shipping_addr", "user", userID, addressID)
}
