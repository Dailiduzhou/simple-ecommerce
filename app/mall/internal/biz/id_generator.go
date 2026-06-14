package biz

// IDGenerator produces unique identifiers that are safe to use as
// human-facing references such as out_trade_no, order_no, or request_id.
//
// The interface lives in the domain layer so that usecases can depend on
// the abstraction (DDD) while the concrete implementation (snowflake,
// uuid, segment, etc.) is injected through wire in the data layer.
type IDGenerator interface {
	GenerateString() string
}
