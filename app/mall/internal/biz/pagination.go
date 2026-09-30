package biz

import (
	"math"

	"github.com/go-kratos/kratos/v2/errors"
)

const MaxPageSize int32 = 100

type Pagination struct{ Limit, Offset int32 }

func NewPage(page, size int32) (Pagination, error) {
	if page < 0 || size < 0 || size > MaxPageSize {
		return Pagination{}, errors.BadRequest("PAGINATION_INVALID", "page must be non-negative and page_size must be in [0,100]")
	}
	if page == 0 {
		page = 1
	}
	if size == 0 {
		size = 20
	}
	offset := int64(page-1) * int64(size)
	if offset > math.MaxInt32 {
		return Pagination{}, errors.BadRequest("PAGINATION_INVALID", "pagination offset is too large")
	}
	return Pagination{Limit: size, Offset: int32(offset)}, nil
}

func NewOffsetPage(limit, offset int32) (Pagination, error) {
	if limit < 0 || limit > MaxPageSize || offset < 0 {
		return Pagination{}, errors.BadRequest("PAGINATION_INVALID", "limit must be in [0,100] and offset must be non-negative")
	}
	if limit == 0 {
		limit = 20
	}
	return Pagination{Limit: limit, Offset: offset}, nil
}
