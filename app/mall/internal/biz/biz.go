package biz

import "github.com/google/wire"

// ProviderSet is biz providers.
var ProviderSet = wire.NewSet(
	NewBrowsingHistoryUsecase, NewPostUsecase, NewCommentUsecase, NewMediaUsecase,
	NewAuthUsecase,
	NewUserUsecase,
	NewShippingAddressUsecase,
	NewProductUsecase,
	NewCategoryUsecase,
	NewEventUsecase,
	NewConfiguredOrderUsecase,
	NewPaymentJobUsecase,
	NewConfiguredPaymentUsecase,
	NewPaymentGateway,
)
