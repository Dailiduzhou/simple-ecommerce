package data

import (
	"crypto/rand"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/conf"
	"github.com/bwmarrin/snowflake"
)

// EnvSnowflakeNodeID is the env var consulted when no conf.Snowflake is
// provided (e.g. in tests or local dev runs without the YAML entry).
const EnvSnowflakeNodeID = "SNOWFLAKE_NODE_ID"

var _ biz.IDGenerator = (*snowflakeGenerator)(nil)

type snowflakeGenerator struct {
	node *snowflake.Node
}

func NewSnowflakeIDGenerator(c *conf.Snowflake) (*snowflakeGenerator, error) {
	nodeID, err := resolveSnowflakeNodeID(c)
	if err != nil {
		return nil, err
	}
	node, err := snowflake.NewNode(nodeID)
	if err != nil {
		return nil, fmt.Errorf("create snowflake node %d: %w", nodeID, err)
	}
	return &snowflakeGenerator{node: node}, nil
}

func (g *snowflakeGenerator) GenerateString() string {
	return g.node.Generate().String()
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

// snowflakeMaxNode mirrors the upper bound enforced by snowflake.NewNode
// (NodeBits=10 by default), kept here so we can return a precise error
// before the package does its own check.
func snowflakeMaxNode() int64 {
	return int64(1)<<snowflake.NodeBits - 1
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
	snowInt64 := g.node.Generate().Int64()

	// %016x 会将 int64 转换为绝对的 16 位小写十六进制字符串
	return fmt.Sprintf("%s%s%016x", prefix, timestamp, snowInt64)
}

// GenerateOrderNo64 生成严格 64 位的订单号
// 格式: 业务前缀(4位) + 时间戳(14位) + 用户ID补齐(8位) + 雪花ID补齐(19位) + 随机串(19位) = 64位
func (g *snowflakeGenerator) GenerateOrderNo64(prefix string, userID int64) string {
	if len(prefix) > 4 {
		prefix = prefix[:4]
	} else if len(prefix) < 4 {
		prefix = fmt.Sprintf("%-4s", prefix)
	}

	timestamp := time.Now().Format("20060102150405")

	// 雪花 ID 原始十进制 (使用 %019d 保证固定 19 位)
	snowInt64 := g.node.Generate().Int64()

	// 生成 19 位安全随机串 (包含大小写字母和数字)
	randomStr := generateSecureRandomString(19)

	return fmt.Sprintf("%s%s%08d%019d%s", prefix, timestamp, userID, snowInt64, randomStr)
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
