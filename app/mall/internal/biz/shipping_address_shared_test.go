package biz

import (
	"context"
	"sync"
	"testing"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/conf"
	"github.com/Dailiduzhou/simple-ecommerce/pkg/phonecrypto"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/stretchr/testify/require"
)

// A singleflight miss may give every reader exactly these same objects.
type sharedShippingAddressRepo struct {
	ShippingAddressRepo
	address   *ShippingAddress
	addresses []ShippingAddress
}

func (r sharedShippingAddressRepo) GetShippingAddress(context.Context, int64, int64) (*ShippingAddress, error) {
	return r.address, nil
}

func (r sharedShippingAddressRepo) ListShippingAddressesByUser(context.Context, int64, int32, int32) ([]ShippingAddress, error) {
	return r.addresses, nil
}

func TestShippingAddressDecryptionDoesNotMutateSharedResults(t *testing.T) {
	for _, list := range []bool{false, true} {
		name := "single"
		if list {
			name = "list"
		}
		t.Run(name, func(t *testing.T) {
			const phone, secret = "13800138000", "phone-secret"
			encrypted, err := phonecrypto.EncryptPhone(phone, []byte(secret))
			require.NoError(t, err)
			shared := ShippingAddress{ID: 9, UserID: 2, ReceiverPhoneEncrypt: encrypted}
			repo := sharedShippingAddressRepo{address: &shared, addresses: []ShippingAddress{shared}}
			uc := NewShippingAddressUsecase(repo, &conf.Auth{PhoneSecret: secret}, log.DefaultLogger)
			const readers = 64
			start := make(chan struct{})
			type result struct {
				address *ShippingAddress
				err     error
			}
			results := make(chan result, readers)
			var wg sync.WaitGroup
			for range readers {
				wg.Go(func() {
					<-start
					if list {
						addresses, err := uc.ListShippingAddressesByUser(context.Background(), 2, 1, 20)
						if err != nil || len(addresses) != 1 {
							results <- result{err: err}
							return
						}
						results <- result{address: &addresses[0]}
						return
					}
					address, err := uc.GetShippingAddress(context.Background(), 9, 2)
					results <- result{address, err}
				})
			}
			close(start)
			wg.Wait()
			close(results)
			var addresses []*ShippingAddress
			for result := range results {
				require.NoError(t, result.err)
				require.NotNil(t, result.address)
				require.Equal(t, phone, result.address.ReceiverPhone)
				require.Equal(t, encrypted, result.address.ReceiverPhoneEncrypt)
				addresses = append(addresses, result.address)
			}
			require.Empty(t, shared.ReceiverPhone, "do not write plaintext into the shared address")
			require.Empty(t, repo.addresses[0].ReceiverPhone, "do not write plaintext into the shared list")
			addresses[0].ReceiverPhone = "request-local change"
			for _, address := range addresses[1:] {
				require.Equal(t, phone, address.ReceiverPhone, "callers must own independent results")
			}
		})
	}
}

func TestShippingAddressDecryptionPreservesNilResults(t *testing.T) {
	uc := NewShippingAddressUsecase(sharedShippingAddressRepo{}, &conf.Auth{}, log.DefaultLogger)
	address, err := uc.GetShippingAddress(context.Background(), 9, 2)
	require.NoError(t, err)
	require.Nil(t, address)
	addresses, err := uc.ListShippingAddressesByUser(context.Background(), 2, 1, 20)
	require.NoError(t, err)
	require.Nil(t, addresses)
}
