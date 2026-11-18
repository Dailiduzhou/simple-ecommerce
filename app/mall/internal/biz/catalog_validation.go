package biz

import (
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/go-kratos/kratos/v2/errors"
	"github.com/shopspring/decimal"
)

const maxProductNumberBytes = 64

func invalidProductPrice() error {
	return errors.BadRequest("PRODUCT_PRICE_INVALID", "price must be non-negative, use at most two decimal places and fit int64 minor units")
}

func invalidProductDiscount() error {
	return errors.BadRequest("PRODUCT_DISCOUNT_INVALID", "discount must be in (0,1] with at most two decimal places")
}

// boundedProductDecimal checks input complexity before decimal parsing or any
// rescaling. Scientific notation remains supported, but cannot request an
// unbounded power of ten. All errors deliberately omit the original input.
func boundedProductDecimal(raw string) (decimal.Decimal, bool) {
	if len(raw) == 0 || len(raw) > maxProductNumberBytes {
		return decimal.Decimal{}, false
	}
	if i := strings.IndexAny(raw, "eE"); i >= 0 {
		exponent := raw[i+1:]
		// ParseInt itself is bounded by the already checked input byte length.
		n, err := strconv.ParseInt(exponent, 10, 32)
		if err != nil || n < -64 || n > 64 {
			return decimal.Decimal{}, false
		}
	}
	value, err := decimal.NewFromString(raw)
	return value, err == nil && productDecimalComplexityOK(value)
}

// Guard the exported decimal entry points too, BEFORE Shift, comparisons or
// rounding. A parsed 64-byte mantissa with exponent -64 may have internal scale
// below -64; 128 covers that range. Coefficient inspection never builds a power
// of ten (NumDigits does), and rejects oversized internal coefficients.
func productDecimalComplexityOK(value decimal.Decimal) bool {
	return value.Exponent() >= -128 && value.Exponent() <= 128 && value.Coefficient().BitLen() <= 213
}

func ParseProductPrice(raw string) (decimal.Decimal, error) {
	value, ok := boundedProductDecimal(raw)
	if !ok {
		return decimal.Decimal{}, invalidProductPrice()
	}
	if _, err := ProductPriceMinor(value); err != nil {
		return decimal.Decimal{}, err
	}
	return value, nil
}

func ParseProductDiscount(raw string) (decimal.Decimal, error) {
	value, ok := boundedProductDecimal(raw)
	if !ok {
		return decimal.Decimal{}, invalidProductDiscount()
	}
	if err := ProductDiscount(value); err != nil {
		return decimal.Decimal{}, err
	}
	return value, nil
}

// ValidateProductInput is shared by creation and content editing. The caller
// persists its normalized name; stock is checked separately on creation only.
func ValidateProductInput(categoryID int64, name, description string, cover, images []MediaInfo) (string, error) {
	name = strings.TrimSpace(name)
	if !utf8.ValidString(name) || strings.ContainsRune(name, 0) || utf8.RuneCountInString(name) < 1 || utf8.RuneCountInString(name) > 255 {
		return "", errors.BadRequest("PRODUCT_NAME_INVALID", "name must contain 1 to 255 Unicode characters")
	}
	if categoryID <= 0 {
		return "", errors.BadRequest("PRODUCT_CATEGORY_INVALID", "category must be positive")
	}
	if !utf8.ValidString(description) || strings.ContainsRune(description, 0) || utf8.RuneCountInString(description) > 10000 {
		return "", errors.BadRequest("PRODUCT_DESCRIPTION_INVALID", "description must contain at most 10000 Unicode characters")
	}
	if len(cover) > 1 || len(images) > 9 {
		return "", errors.BadRequest("PRODUCT_IMAGES_INVALID", "at most one cover and nine detail images are allowed")
	}
	seen := make(map[string]bool)
	for _, group := range [][]MediaInfo{cover, images} {
		for _, image := range group {
			if !validCatalogURL(image.OssURL) || len(image.BucketName) > 255 || len(image.ObjectKey) > 1024 || len(image.ContentType) > 255 || image.Size < 0 ||
				!utf8.ValidString(image.BucketName+image.ObjectKey+image.ContentType) || strings.ContainsRune(image.BucketName+image.ObjectKey+image.ContentType, 0) ||
				(image.BucketName == "") != (image.ObjectKey == "") || (image.OssURL == "" && image.ObjectKey == "") {
				return "", errors.BadRequest("PRODUCT_IMAGES_INVALID", "invalid image metadata or URL")
			}
			if image.ObjectKey != "" {
				key := image.BucketName + "\x00" + image.ObjectKey
				if seen[key] {
					return "", errors.BadRequest("PRODUCT_IMAGES_INVALID", "duplicate image resource")
				}
				seen[key] = true
			}
		}
	}
	return name, nil
}

func validCatalogURL(raw string) bool {
	if raw == "" {
		return true
	}
	if len(raw) > 4096 || !utf8.ValidString(raw) || strings.ContainsAny(raw, "\x00\r\n\t ") {
		return false
	}
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.Fragment == "" && u.Opaque == ""
}
