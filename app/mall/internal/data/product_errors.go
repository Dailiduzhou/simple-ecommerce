package data

import (
	stderrors "errors"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/go-kratos/kratos/v2/errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Translate only constraints we own and expect; infrastructure failures and
// unknown constraints must not be disguised as invalid client input.
func productWriteError(err error) error {
	if err == nil {
		return nil
	}
	var domainError *errors.Error
	if stderrors.As(err, &domainError) {
		return err
	}
	if stderrors.Is(err, pgx.ErrNoRows) {
		return biz.ErrProductNotFound
	}
	var pg *pgconn.PgError
	if !stderrors.As(err, &pg) {
		return errors.InternalServer("PRODUCT_WRITE_FAILED", "cannot save product").WithCause(err)
	}
	if pg.Code == "23503" {
		switch pg.ConstraintName {
		case "fk_product_category":
			return errors.NotFound("CATEGORY_NOT_FOUND", "category not found")
		case "fk_product_merchant":
			return errors.NotFound("MERCHANT_NOT_FOUND", "merchant not found")
		}
	}
	if pg.Code == "23514" {
		switch pg.ConstraintName {
		case "products_name_check":
			return errors.BadRequest("PRODUCT_NAME_INVALID", "invalid product name")
		case "products_description_check":
			return errors.BadRequest("PRODUCT_DESCRIPTION_INVALID", "invalid product description")
		case "products_stock_check":
			return errors.BadRequest("PRODUCT_STOCK_INVALID", "stock must be non-negative")
		case "products_price_minor_check":
			return errors.BadRequest("PRODUCT_PRICE_INVALID", "invalid product price")
		case "products_discount_check":
			return errors.BadRequest("PRODUCT_DISCOUNT_INVALID", "invalid product discount")
		case "products_status_check":
			return errors.BadRequest("PRODUCT_STATUS_INVALID", "invalid product status")
		}
	}
	// Preserve the real failure for internal diagnostics, but never serialize
	// a driver message (SQL, connection details or input) as a public message.
	return errors.InternalServer("PRODUCT_WRITE_FAILED", "cannot save product").WithCause(err)
}
