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
	log  *log.Helper
}

func NewShippingAddressRepo(data *Data, logger log.Logger) *ShippingAddressRepo {
	return &ShippingAddressRepo{data: data, log: log.NewHelper(logger)}
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
	r.setCache(ctx, fmt.Sprintf("user:%d", bizUser.ID), bizUser)
	r.setCache(ctx, fmt.Sprintf("user:phone:%s", phoneHash), bizUser)
	return bizUser, nil
}

func (r *UserRepo) GetUserByID(ctx context.Context, id int64) (*biz.User, error) {
	cacheKey := fmt.Sprintf("user:%d", id)

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
		r.setCache(ctx, fmt.Sprintf("user:phone:%s", bizUser.PhoneHash), bizUser)
		return bizUser, nil
	})

	if err != nil {
		return nil, err
	}

	return val.(*biz.User), nil
}

func (r *UserRepo) GetUserByPhoneHash(ctx context.Context, phoneHash string) (*biz.User, error) {
	cacheKey := fmt.Sprintf("user:phone:%s", phoneHash)

	u, err := r.getCache(ctx, cacheKey)
	if err == nil {
		return u, nil
	}
	if !stderrors.Is(err, redis.Nil) {
		r.log.WithContext(ctx).Errorf("get user phone cache: %v", err)
	}

	sfKey := fmt.Sprintf("sf:user:phone:%s", phoneHash)
	val, err, _ := r.data.sg.Do(sfKey, func() (any, error) {
		userDoublecheck, err := r.getCache(ctx, cacheKey)
		if err == nil {
			return userDoublecheck, nil
		}
		u, err := r.data.q.GetUserByPhoneHash(ctx, phoneHash)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return (*biz.User)(nil), nil
			}
			return nil, err
		}
		bizUser := toBizUser(u)
		r.setCache(ctx, cacheKey, bizUser)
		r.setCache(ctx, fmt.Sprintf("user:%d", bizUser.ID), bizUser)
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
	r.deleteCache(ctx, fmt.Sprintf("user:%d", id))
	r.deleteCache(ctx, fmt.Sprintf("user:phone:%s", bizUser.PhoneHash))
	r.setCache(ctx, fmt.Sprintf("user:%d", id), bizUser)
	r.setCache(ctx, fmt.Sprintf("user:phone:%s", bizUser.PhoneHash), bizUser)
	return bizUser, nil
}

func (r *UserRepo) DeleteUser(ctx context.Context, id int64) error {
	u, err := r.GetUserByID(ctx, id)
	if err != nil {
		return err
	}
	err = r.data.q.DeleteUser(ctx, id)
	if err != nil {
		return err
	}
	r.deleteCache(ctx, fmt.Sprintf("user:%d", id))
	if u != nil {
		r.deleteCache(ctx, fmt.Sprintf("user:phone:%s", u.PhoneHash))
	}
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
	data, err := json.Marshal(user)
	if err != nil {
		r.log.WithContext(ctx).Errorf("marshal user cache: %v", err)
		return
	}
	jitter := time.Duration(mrand.Intn(10)) * time.Minute
	exp := jitter + 10*time.Minute
	r.data.rdb.Set(ctx, key, data, exp)
}

func (r *UserRepo) deleteCache(ctx context.Context, key string) {
	if err := r.data.rdb.Del(ctx, key).Err(); err != nil {
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
	data, err := json.Marshal(sa)
	if err != nil {
		r.log.WithContext(ctx).Errorf("marshal shipping address cache: %v", err)
		return
	}
	jitter := time.Duration(mrand.Intn(10)) * time.Minute
	exp := jitter + 10*time.Minute
	r.data.rdb.Set(ctx, key, data, exp)
}

func (r *ShippingAddressRepo) setListCache(ctx context.Context, key string, sas []biz.ShippingAddress) {
	data, err := json.Marshal(sas)
	if err != nil {
		r.log.WithContext(ctx).Errorf("marshal shipping address list cache: %v", err)
		return
	}
	jitter := time.Duration(mrand.Intn(10)) * time.Minute
	exp := jitter + 10*time.Minute
	r.data.rdb.Set(ctx, key, data, exp)
}

func (r *ShippingAddressRepo) deleteCache(ctx context.Context, key string) {
	if err := r.data.rdb.Del(ctx, key).Err(); err != nil {
		r.log.WithContext(ctx).Errorf("delete cache %s", key)
		return
	}
}

func (r *ShippingAddressRepo) deleteListCache(ctx context.Context, key string) {
	if err := r.data.rdb.Del(ctx, key).Err(); err != nil {
		r.log.WithContext(ctx).Errorf("delete list cache %s", key)
		return
	}
}

func (r *ShippingAddressRepo) CreateShippingAddress(ctx context.Context, userID int64, receiverName string, receiverPhoneHash string, receiverPhoneEncrypt string, province string, city string, district string, detailAddress string, addressTag string, isDefault bool) (*biz.ShippingAddress, error) {
	isValid := addressTag != ""

	sd, err := r.data.q.CreateShippingAddress(ctx, db.CreateShippingAddressParams{
		UserID: userID, ReceiverName: receiverName, ReceiverPhoneHash: receiverPhoneHash, ReceiverPhoneEncrypt: receiverPhoneEncrypt, Province: province, City: city, District: district, DetailAddress: detailAddress, AddressTag: pgtype.Text{String: addressTag, Valid: isValid}, IsDefault: isDefault,
	})
	if err != nil {
		return nil, err
	}
	result := toBizShippingAddress(sd)
	r.setCache(ctx, fmt.Sprintf("shipping_addr:%d", result.ID), &result)
	r.deleteListCache(ctx, fmt.Sprintf("shipping_addr:user:%d", userID))
	return &result, nil
}

func (r *ShippingAddressRepo) ListShippingAddressesByUser(ctx context.Context, userID int64) ([]biz.ShippingAddress, error) {
	cacheKey := fmt.Sprintf("shipping_addr:user:%d", userID)

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
	cacheKey := fmt.Sprintf("shipping_addr:%d", id)

	sa, err := r.getCache(ctx, cacheKey)
	if err == nil {
		return sa, nil
	}
	if !stderrors.Is(err, redis.Nil) {
		r.log.WithContext(ctx).Errorf("get shipping address cache: %v", err)
	}

	sfKey := fmt.Sprintf("sf:shipping_addr:%d", id)
	val, err, _ := r.data.sg.Do(sfKey, func() (any, error) {
		sa, err := r.getCache(ctx, cacheKey)
		if err == nil {
			return sa, nil
		}
		dbSa, err := r.data.q.GetShippingAddress(ctx, db.GetShippingAddressParams{
			ID:     id,
			UserID: userID,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return (*biz.User)(nil), nil
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

	sa, err := r.data.q.UpdateShippingAddress(ctx, db.UpdateShippingAddressParams{
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
	r.deleteCache(ctx, fmt.Sprintf("shipping_addr:%d", id))
	r.deleteListCache(ctx, fmt.Sprintf("shipping_addr:user:%d", userID))
	r.setCache(ctx, fmt.Sprintf("shipping_addr:%d", id), &result)
	return &result, nil
}

func (r *ShippingAddressRepo) SetDefaultShippingAddress(ctx context.Context, id int64, userID int64) error {
	err := r.data.q.ClearDefaultShippingAddress(ctx, userID)
	if err != nil {
		return err
	}
	err = r.data.q.SetDefaultShippingAddress(ctx, db.SetDefaultShippingAddressParams{
		ID:     id,
		UserID: userID,
	})
	if err != nil {
		return err
	}
	r.deleteListCache(ctx, fmt.Sprintf("shipping_addr:user:%d", userID))
	return nil
}

func (r *ShippingAddressRepo) DeleteShippingAddress(ctx context.Context, id int64, userID int64) error {
	err := r.data.q.DeleteShippingAddress(ctx, db.DeleteShippingAddressParams{
		ID:     id,
		UserID: userID,
	})
	if err != nil {
		return err
	}
	r.deleteCache(ctx, fmt.Sprintf("shipping_addr:%d", id))
	r.deleteListCache(ctx, fmt.Sprintf("shipping_addr:user:%d", userID))
	return nil
}
