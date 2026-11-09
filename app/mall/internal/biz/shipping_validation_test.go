package biz

import (
	"context"
	"strings"
	"testing"

	"github.com/Dailiduzhou/simple-ecommerce/pkg/phonecrypto"
	"github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/stretchr/testify/require"
)

func TestShippingAddressValidation(t *testing.T) {
	valid := []string{"收件人", "13800138000", "省", "市", "区", "街道1号", "家"}
	tests := []struct {
		name  string
		index int
		value string
	}{
		{"empty phone", 1, ""}, {"whitespace phone", 1, " "},
		{"invalid phone", 1, "1380013800x"}, {"oversized phone", 1, strings.Repeat("1", 1000)},
		{"blank receiver", 0, " \t "}, {"oversized receiver", 0, strings.Repeat("人", 51)},
		{"missing province", 2, ""}, {"oversized province", 2, strings.Repeat("省", 31)},
		{"missing city", 3, ""}, {"oversized city", 3, strings.Repeat("市", 31)},
		{"missing district", 4, ""}, {"oversized district", 4, strings.Repeat("区", 31)},
		{"blank detail", 5, "　 "}, {"oversized detail", 5, strings.Repeat("路", 256)},
		{"oversized tag", 6, strings.Repeat("家", 21)}, {"nul", 5, "road\x00"},
		{"invalid UTF8", 0, string([]byte{0xff})},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			values := append([]string(nil), valid...)
			values[tc.index] = tc.value
			// A nil repo makes any attempted write fail the test: reject before
			// encryption or persistence, on both creation and update.
			uc := NewShippingAddressUsecase(nil, testUserAuth(), log.DefaultLogger)
			_, err := uc.CreateShippingAddress(context.Background(), 1, values[0], values[1], values[2], values[3], values[4], values[5], values[6], false)
			require.Error(t, err)
			require.EqualValues(t, 400, errors.FromError(err).Code)
			_, err = uc.UpdateShippingAddress(context.Background(), 2, 1, values[0], values[1], values[2], values[3], values[4], values[5], values[6])
			require.Error(t, err)
			require.EqualValues(t, 400, errors.FromError(err).Code)
		})
	}
	require.NoError(t, ValidateShippingAddress(strings.Repeat("人", 50), valid[1], strings.Repeat("省", 30), strings.Repeat("市", 30), strings.Repeat("区", 30), strings.Repeat("路", 255), strings.Repeat("家", 20)))
	require.NoError(t, ValidateShippingAddress(valid[0], valid[1], valid[2], valid[3], valid[4], valid[5], ""))
}

type validatedShippingRepo struct {
	ShippingAddressRepo
	hash, cipher string
}

func (r *validatedShippingRepo) CreateShippingAddress(_ context.Context, _ int64, _, hash, cipher, _, _, _, _, _ string, _ bool) (*ShippingAddress, error) {
	r.hash, r.cipher = hash, cipher
	return &ShippingAddress{ReceiverPhoneEncrypt: cipher}, nil
}
func (r *validatedShippingRepo) UpdateShippingAddress(_ context.Context, _, _ int64, _, hash, cipher, _, _, _, _, _ string) (*ShippingAddress, error) {
	r.hash, r.cipher = hash, cipher
	return &ShippingAddress{ReceiverPhoneEncrypt: cipher}, nil
}
func TestValidShippingWritesEncryptPhone(t *testing.T) {
	r := &validatedShippingRepo{}
	uc := NewShippingAddressUsecase(r, testUserAuth(), log.DefaultLogger)
	create, err := uc.CreateShippingAddress(context.Background(), 1, "收件人", "13800138000", "省", "市", "区", "1号", "", false)
	require.NoError(t, err)
	require.Equal(t, "13800138000", create.ReceiverPhone)
	updated, err := uc.UpdateShippingAddress(context.Background(), 2, 1, "收件人", "13900139000", "省", "市", "区", "2号", "家")
	require.NoError(t, err)
	require.Equal(t, "13900139000", updated.ReceiverPhone)
	secret := []byte(testUserAuth().PhoneSecret)
	require.Equal(t, phonecrypto.HashPhone("13900139000", secret), r.hash)
	plain, err := phonecrypto.DecryptPhone(r.cipher, secret)
	require.NoError(t, err)
	require.Equal(t, "13900139000", plain)
}
