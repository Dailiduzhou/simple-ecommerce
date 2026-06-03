package data

import (
	"context"
	"crypto/rand"
	"encoding/hex"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	"github.com/go-kratos/kratos/v2/errors"
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
	return toBizUser(u), nil
}

func (r *UserRepo) GetUserByID(ctx context.Context, id int64) (*biz.User, error) {
	u, err := r.data.q.GetUserByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return toBizUser(u), nil
}

func (r *UserRepo) GetUserByPhoneHash(ctx context.Context, phoneHash string) (*biz.User, error) {
	u, err := r.data.q.GetUserByPhoneHash(ctx, phoneHash)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return toBizUser(u), nil
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
	return toBizUser(u), nil
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

func (r *UserRepo) DeleteUser(ctx context.Context, id int64) error {
	err := r.data.q.DeleteUser(ctx, id)
	if err != nil {
		return err
	}
	return nil
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
	return &result, nil
}

func (r *ShippingAddressRepo) ListShippingAddressesByUser(ctx context.Context, userID int64) ([]biz.ShippingAddress, error) {
	sas, err := r.data.q.ListShippingAddressesByUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	var bizSas []biz.ShippingAddress
	for _, sa := range sas {
		bizSas = append(bizSas, toBizShippingAddress(sa))
	}
	return bizSas, nil
}

func (r *ShippingAddressRepo) GetShippingAddress(ctx context.Context, id int64, userID int64) (*biz.ShippingAddress, error) {
	sa, err := r.data.q.GetShippingAddress(ctx, db.GetShippingAddressParams{
		ID:     id,
		UserID: userID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	result := toBizShippingAddress(sa)
	return &result, nil
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
	return &result, nil
}

func (r *ShippingAddressRepo) SetDefaultShippingAddress(ctx context.Context, id int64, userID int64) error {
	err := r.data.q.ClearDefaultShippingAddress(ctx, userID)
	if err != nil {
		return err
	}
	return r.data.q.SetDefaultShippingAddress(ctx, db.SetDefaultShippingAddressParams{
		ID:     id,
		UserID: userID,
	})
}

func (r *ShippingAddressRepo) DeleteShippingAddress(ctx context.Context, id int64, userID int64) error {
	return r.data.q.DeleteShippingAddress(ctx, db.DeleteShippingAddressParams{
		ID:     id,
		UserID: userID,
	})
}
