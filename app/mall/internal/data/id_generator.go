package data

import (
	"crypto/rand"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/conf"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/observability"
)

// EnvSnowflakeNodeID is the env var consulted when no conf.Snowflake is
// provided (e.g. in tests or local dev runs without the YAML entry).
const EnvSnowflakeNodeID = "SNOWFLAKE_NODE_ID"

// Layout is compatible with Twitter's reference snowflake (and therefore with
// the ids the previous bwmarrin implementation produced): 41-bit milliseconds
// since snowflakeEpochMs, 10-bit node id, 12-bit per-millisecond sequence.
const (
	snowflakeEpochMs   = int64(1288834974657) // 2010-11-04T01:42:54.657Z
	snowflakeNodeBits  = 10
	snowflakeStepBits  = 12
	snowflakeNodeShift = snowflakeStepBits
	snowflakeTimeShift = snowflakeNodeBits + snowflakeStepBits
	snowflakeStepMask  = int64(1)<<snowflakeStepBits - 1
)

var _ biz.IDGenerator = (*snowflakeGenerator)(nil)

// snowflakeGenerator issues monotonically increasing ids from one node.
//
// The previous implementation (bwmarrin/snowflake v0.3.0) rewound its clock to
// the rolled-back wall time, so an NTP step could reissue an already handed out
// (time, node, sequence) triple and collide on unique indexes. This generator
// never moves its clock backwards: while wall time is behind the last timestamp
// it handed out, it keeps issuing from that timestamp (advancing the sequence,
// borrowing the next millisecond when the sequence is exhausted). Uniqueness
// therefore holds for any backwards step, and forward steps are honoured
// because the timestamp only ever increases.
type snowflakeGenerator struct {
	mu        sync.Mutex
	nodeID    int64
	wallMs    int64
	lastMs    int64
	step      int64
	now       func() time.Time
	rollbacks int64
}

func NewSnowflakeIDGenerator(c *conf.Snowflake) (*snowflakeGenerator, error) {
	nodeID, err := resolveSnowflakeNodeID(c)
	if err != nil {
		return nil, err
	}
	return &snowflakeGenerator{nodeID: nodeID, now: time.Now}, nil
}

func (g *snowflakeGenerator) GenerateString() string {
	return strconv.FormatInt(g.nextID(), 10)
}

// nextID returns the next unique, strictly increasing id.
func (g *snowflakeGenerator) nextID() int64 {
	g.mu.Lock()
	defer g.mu.Unlock()

	rawMs := g.now().UnixMilli()
	if rawMs < g.wallMs {
		// The host clock itself moved backwards (NTP step): report it. This is
		// distinct from the monotonic timestamp below merely running ahead of
		// the wall clock after a sequence overflow.
		g.rollbacks++
		observability.IDGeneratorClockRollback()
	} else {
		g.wallMs = rawMs
	}
	nowMs := max(rawMs, g.lastMs)
	if nowMs == g.lastMs {
		g.step++
		if g.step > snowflakeStepMask {
			// Sequence exhausted for this millisecond: borrow the next one so
			// ids stay unique and increasing even above 4096 ids/ms.
			g.lastMs++
			g.step = 0
		}
	} else {
		g.lastMs = nowMs
		g.step = 0
	}
	return (g.lastMs-snowflakeEpochMs)<<snowflakeTimeShift |
		g.nodeID<<snowflakeNodeShift |
		g.step
}

// clockRollbacks reports how many backwards clock steps have been absorbed.
func (g *snowflakeGenerator) clockRollbacks() int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.rollbacks
}

func resolveSnowflakeNodeID(c *conf.Snowflake) (int64, error) {
	if c != nil && c.NodeId > 0 {
		return int64(c.NodeId), nil
	}
	raw := os.Getenv(EnvSnowflakeNodeID)
	if raw == "" {
		return 0, fmt.Errorf("snowflake node_id is required: set %s or conf.snowflake.node_id", EnvSnowflakeNodeID)
	}
	nodeID, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid %s=%q: %w", EnvSnowflakeNodeID, raw, err)
	}
	if nodeID < 0 || nodeID > snowflakeMaxNode() {
		return 0, fmt.Errorf("snowflake node_id out of range [0,%d]: got %d", snowflakeMaxNode(), nodeID)
	}
	return nodeID, nil
}

// snowflakeMaxNode mirrors the upper bound of the 10-bit node field, kept here
// so we can return a precise error before any id is generated.
func snowflakeMaxNode() int64 {
	return int64(1)<<snowflakeNodeBits - 1
}

// GenerateOrderNo32 生成严格 32 位的订单号
// 格式: 业务前缀(2位) + 时间戳(14位) + 雪花ID的16进制(16位) = 32位
func (g *snowflakeGenerator) GenerateOrderNo32(prefix string) string {
	if len(prefix) > 2 {
		prefix = prefix[:2] // 强行截断保证格式安全
	} else if len(prefix) < 2 {
		prefix = fmt.Sprintf("%-2s", prefix) // 不足补空格，或按需换成补 '0'
	}

	timestamp := time.Now().Format("20060102150405")
	snowInt64 := g.nextID()

	// %016x 会将 int64 转换为绝对的 16 位小写十六进制字符串
	return fmt.Sprintf("%s%s%016x", prefix, timestamp, snowInt64)
}

// GenerateOrderNo64 生成严格 64 位的订单号
// 格式: 业务前缀(4位) + 时间戳(14位) + 用户ID base36 补齐(8位) + 雪花ID补齐(19位) + 随机串(19位) = 64位
//
// 用户ID 使用 base36 而不是 %08d：十进制补零在用户量达到 10^8 时会占满
// 8 位并撑破 VARCHAR(64) 列（base36 的 8 位可容纳 36^8 ≈ 2.8×10^12 个用户）。
func (g *snowflakeGenerator) GenerateOrderNo64(prefix string, userID int64) string {
	if len(prefix) > 4 {
		prefix = prefix[:4]
	} else if len(prefix) < 4 {
		prefix = fmt.Sprintf("%-4s", prefix)
	}

	timestamp := time.Now().Format("20060102150405")

	// 雪花 ID 原始十进制 (使用 %019d 保证固定 19 位)
	snowInt64 := g.nextID()

	// 生成 19 位安全随机串 (包含大小写字母和数字)
	randomStr := generateSecureRandomString(19)

	return fmt.Sprintf("%s%s%s%019d%s", prefix, timestamp, base36UserID(userID), snowInt64, randomStr)
}

// base36UserID renders a user id as exactly 8 zero-padded base-36 characters.
// Ids wider than the field keep their low-order digits; the snowflake and
// random suffixes keep the full order number unique either way.
func base36UserID(userID int64) string {
	const width = 8
	encoded := "0"
	if userID > 0 {
		encoded = strings.ToUpper(strconv.FormatInt(userID, 36))
	}
	if len(encoded) > width {
		encoded = encoded[len(encoded)-width:]
	}
	return strings.Repeat("0", width-len(encoded)) + encoded
}

// generateSecureRandomString 生成指定长度的密码学安全随机字符串
func generateSecureRandomString(length int) string {
	const charset = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, length)
	if _, err := rand.Read(b); err != nil {
		// 理论上 crypto/rand 极小概率失败，若失败退化为纳秒时间戳防止 panic
		return fmt.Sprintf("%019d", time.Now().UnixNano())[:length]
	}
	for i := 0; i < length; i++ {
		b[i] = charset[int(b[i])%len(charset)]
	}
	return string(b)
}
