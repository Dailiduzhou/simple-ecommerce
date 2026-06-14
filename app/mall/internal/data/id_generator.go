package data

import (
	"fmt"
	"os"
	"strconv"
	"sync"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/conf"
	"github.com/bwmarrin/snowflake"
)

// EnvSnowflakeNodeID is the env var consulted when no conf.Snowflake is
// provided (e.g. in tests or local dev runs without the YAML entry).
const EnvSnowflakeNodeID = "SNOWFLAKE_NODE_ID"

var _ biz.IDGenerator = (*snowflakeGenerator)(nil)

// snowflakeGenerator wraps bwmarrin/snowflake to produce string IDs.
// It is safe for concurrent use across goroutines.
type snowflakeGenerator struct {
	node *snowflake.Node
	mu   sync.Mutex
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
	g.mu.Lock()
	defer g.mu.Unlock()
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
