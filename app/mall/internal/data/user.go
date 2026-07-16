package data

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	stderrors "errors"
	"fmt"
	mrand "math/rand"
	"time"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	"github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/redis/go-redis/v9"
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
	u, err := r.data.q.CreateUser(ctx, db.CreateUserParams{
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
	r.setCache(ctx, redisKey("user", bizUser.ID), bizUser)
	return bizUser, nil
}

func (r *UserRepo) GetUserByID(ctx context.Context, id int64) (*biz.User, error) {
	cacheKey := redisKey("user", id)

	u, err := r.getCache(ctx, cacheKey)
	if err == nil {
		return u, nil
	}
	if !stderrors.Is(err, redis.Nil) {
		r.log.WithContext(ctx).Errorf("get user cache: %v", err)
	}

	sfKey := fmt.Sprintf("sf:user:%d", id)
	val, err, _ := r.data.sg.Do(sfKey, func() (any, error) {
		userDoublecheck, err := r.getCache(ctx, cacheKey)
		if err == nil {
			return userDoublecheck, nil
		}
		u, err := r.data.q.GetUserByID(ctx, id)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return (*biz.User)(nil), nil
			}
			return nil, err
		}
		bizUser := toBizUser(u)
		r.setCache(ctx, cacheKey, bizUser)
		return bizUser, nil
	})

	if err != nil {
		return nil, err
	}

	return val.(*biz.User), nil
}

func (r *UserRepo) GetUserByPhoneHash(ctx context.Context, phoneHash string) (*biz.User, error) {
	sfKey := fmt.Sprintf("sf:user:phone:%s", phoneHash)
	val, err, _ := r.data.sg.Do(sfKey, func() (any, error) {
		u, err := r.data.q.GetUserByPhoneHash(ctx, phoneHash)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return (*biz.User)(nil), nil
			}
			return nil, err
		}
		bizUser := toBizUser(u)
		r.setCache(ctx, redisKey("user", bizUser.ID), bizUser)
		return bizUser, nil
	})

	if err != nil {
		return nil, err
	}

	return val.(*biz.User), nil
}

func (r *UserRepo) UpdateUser(ctx context.Context, id int64, nickname, realName string) (*biz.User, error) {
	u, err := r.data.q.UpdateUser(ctx, db.UpdateUserParams{
		ID:       id,
		Nickname: nickname,
		RealName: realName,
	})
	if err != nil {
		return nil, err
	}
	bizUser := toBizUser(u)
	r.deleteCache(ctx, redisKey("user", id))
	r.setCache(ctx, redisKey("user", id), bizUser)
	return bizUser, nil
}

func (r *UserRepo) DeleteUser(ctx context.Context, id int64) error {
	u, err := r.data.q.GetUserByID(ctx, id)
	if err != nil {
		return err
	}
	err = r.data.q.DeleteUser(ctx, id)
	if err != nil {
		return err
	}
	r.deleteCache(ctx, redisKey("user", id))
	r.deleteCache(ctx, redisKey("user", "phone", u.PhoneHash))
	return nil
}

func (r *UserRepo) getCache(ctx context.Context, key string) (*biz.User, error) {
	val, err := r.data.rdb.Get(ctx, key).Bytes()
	if err != nil {
		return nil, err
	}
	var user biz.User
	if err := json.Unmarshal(val, &user); err != nil {
		return nil, err
	}
	return &user, nil
}

func (r *UserRepo) setCache(ctx context.Context, key string, user *biz.User) {
	profile := struct {
		ID        int64
		Nickname  string
		RealName  string
		Role      string
		CreatedAt time.Time
		UpdatedAt time.Time
	}{user.ID, user.Nickname, user.RealName, user.Role, user.CreatedAt, user.UpdatedAt}
	data, err := json.Marshal(profile)
	if err != nil {
		r.log.WithContext(ctx).Errorf("marshal user cache: %v", err)
		return
	}
	jitter := time.Duration(mrand.Intn(10)) * time.Minute
	exp := jitter + 10*time.Minute
	if err := r.data.rdb.Set(ctx, key, data, exp).Err(); err != nil {
		r.log.WithContext(ctx).Errorw("msg", "write user profile cache failed", "key", key, "error", err)
	}
}

func (r *UserRepo) deleteCache(ctx context.Context, key string) {
	if err := r.data.rdb.Unlink(ctx, key).Err(); err != nil {
		r.log.WithContext(ctx).Errorf("delete cache %s", key)
		return
	}
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
	val, err := r.data.rdb.Get(ctx, key).Bytes()
	if err != nil {
		return nil, err
	}
	var sa biz.ShippingAddress
	if err := json.Unmarshal(val, &sa); err != nil {
		return nil, err
	}
	return &sa, nil
}

func (r *ShippingAddressRepo) getListCache(ctx context.Context, key string) ([]biz.ShippingAddress, error) {
	val, err := r.data.rdb.Get(ctx, key).Bytes()
	if err != nil {
		return nil, err
	}
	var sas []biz.ShippingAddress
	if err := json.Unmarshal(val, &sas); err != nil {
		return nil, err
	}
	return sas, nil
}

func (r *ShippingAddressRepo) setCache(ctx context.Context, key string, sa *biz.ShippingAddress) {
	afterCommit(ctx, func() {
		data, err := json.Marshal(sa)
		if err == nil {
			err = r.data.rdb.Set(ctx, key, data, time.Duration(mrand.Intn(10))*time.Minute+10*time.Minute).Err()
		}
		if err != nil {
			r.log.WithContext(ctx).Errorw("msg", "write shipping address cache failed", "key", key, "error", err)
		}
	})
}

func (r *ShippingAddressRepo) setListCache(ctx context.Context, key string, sas []biz.ShippingAddress) {
	afterCommit(ctx, func() {
		data, err := json.Marshal(sas)
		if err == nil {
			err = r.data.rdb.Set(ctx, key, data, time.Duration(mrand.Intn(10))*time.Minute+10*time.Minute).Err()
		}
		if err != nil {
			r.log.WithContext(ctx).Errorw("msg", "write shipping address list cache failed", "key", key, "error", err)
		}
	})
}

func (r *ShippingAddressRepo) deleteCache(ctx context.Context, key string) {
	afterCommit(ctx, func() {
		if err := r.data.rdb.Unlink(ctx, key).Err(); err != nil {
			r.log.WithContext(ctx).Errorf("delete cache %s", key)
		}
	})
}

func (r *ShippingAddressRepo) deleteListCache(ctx context.Context, key string) {
	afterCommit(ctx, func() {
		if err := r.data.rdb.Unlink(ctx, key).Err(); err != nil {
			r.log.WithContext(ctx).Errorf("delete list cache %s", key)
		}
	})
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
	r.setCache(ctx, shippingAddressCacheKey(userID, result.ID), &result)
	if oldDefaultID > 0 {
		r.deleteCache(ctx, shippingAddressCacheKey(userID, oldDefaultID))
	}
	r.deleteListCache(ctx, redisKey("shipping_addr", "user", userID))
	return &result, nil
}

func (r *ShippingAddressRepo) ListShippingAddressesByUser(ctx context.Context, userID int64) ([]biz.ShippingAddress, error) {
	cacheKey := redisKey("shipping_addr", "user", userID)

	sas, err := r.getListCache(ctx, cacheKey)
	if err == nil {
		return sas, nil
	}
	if !stderrors.Is(err, redis.Nil) {
		r.log.WithContext(ctx).Errorf("get shipping address list cache: %v", err)
	}

	sfKey := fmt.Sprintf("sf:shipping_addr:user:%d", userID)
	val, err, _ := r.data.sg.Do(sfKey, func() (any, error) {
		sas, err := r.getListCache(ctx, cacheKey)
		if err == nil {
			return sas, nil
		}
		dbSas, err := r.data.q.ListShippingAddressesByUser(ctx, userID)
		if err != nil {
			return nil, err
		}
		var bizSas []biz.ShippingAddress
		for _, sa := range dbSas {
			bizSas = append(bizSas, toBizShippingAddress(sa))
		}
		r.setListCache(ctx, cacheKey, bizSas)
		return bizSas, nil
	})

	if err != nil {
		return nil, err
	}

	return val.([]biz.ShippingAddress), nil
}

func (r *ShippingAddressRepo) GetShippingAddress(ctx context.Context, id int64, userID int64) (*biz.ShippingAddress, error) {
	cacheKey := shippingAddressCacheKey(userID, id)

	sa, err := r.getCache(ctx, cacheKey)
	if err == nil {
		if sa.UserID != userID {
			r.deleteCache(ctx, cacheKey)
			return nil, biz.ErrShippingAddressNotFound
		}
		return sa, nil
	}
	if !stderrors.Is(err, redis.Nil) {
		r.log.WithContext(ctx).Errorf("get shipping address cache: %v", err)
	}

	sfKey := fmt.Sprintf("sf:shipping_addr:%d", id)
	val, err, _ := r.data.sg.Do(sfKey, func() (any, error) {
		sa, err := r.getCache(ctx, cacheKey)
		if err == nil {
			if sa.UserID != userID {
				return nil, biz.ErrShippingAddressNotFound
			}
			return sa, nil
		}
		dbSa, err := r.data.q.GetShippingAddress(ctx, db.GetShippingAddressParams{
			ID:     id,
			UserID: userID,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, biz.ErrShippingAddressNotFound
			}
			return nil, err
		}
		bizSa := toBizShippingAddress(dbSa)
		r.setCache(ctx, cacheKey, &bizSa)
		return &bizSa, nil
	})

	if err != nil {
		return nil, err
	}

	return val.(*biz.ShippingAddress), nil
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
	r.deleteCache(ctx, shippingAddressCacheKey(userID, id))
	r.deleteListCache(ctx, redisKey("shipping_addr", "user", userID))
	r.setCache(ctx, shippingAddressCacheKey(userID, id), &result)
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
	r.deleteListCache(ctx, redisKey("shipping_addr", "user", userID))
	r.deleteCache(ctx, shippingAddressCacheKey(userID, id))
	if oldDefaultID > 0 {
		r.deleteCache(ctx, shippingAddressCacheKey(userID, oldDefaultID))
	}
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
	r.deleteCache(ctx, shippingAddressCacheKey(userID, id))
	r.deleteListCache(ctx, redisKey("shipping_addr", "user", userID))
	return nil
}

func shippingAddressCacheKey(userID, addressID int64) string {
	return redisKey("shipping_addr", "user", userID, addressID)
}
