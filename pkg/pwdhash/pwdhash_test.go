package pwdhash

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

func TestHashPasswordUsesTheHardenedCost(t *testing.T) {
	hash, err := HashPassword("secret-pass")
	require.NoError(t, err)

	cost, err := bcrypt.Cost([]byte(hash))
	require.NoError(t, err)
	require.Equal(t, Cost, cost)
	require.GreaterOrEqual(t, cost, 12, "hashes must not fall back to bcrypt.DefaultCost")

	require.NoError(t, ComparePassword(hash, "secret-pass"))
	require.Error(t, ComparePassword(hash, "wrong-pass"))
}

// There is no legacy data, so a hash written with any other cost cannot have
// come from this service and must not authenticate.
func TestComparePasswordRejectsForeignCostHashes(t *testing.T) {
	for _, cost := range []int{4, bcrypt.DefaultCost} {
		foreign, err := bcrypt.GenerateFromPassword([]byte("secret-pass"), cost)
		require.NoError(t, err)
		require.Errorf(t, ComparePassword(string(foreign), "secret-pass"), "cost %d must be rejected", cost)
	}

	// The check reads the hash header only; a higher cost is rejected just as
	// cheaply (this keeps the test from paying for a cost-13 hash).
	higherCost := "$2a$13$" + strings.Repeat("x", 53)
	require.Error(t, ComparePassword(higherCost, "secret-pass"))
}

func TestComparePasswordRejectsMalformedHashes(t *testing.T) {
	for _, stored := range []string{"secret-pass", "", "$2a$12$not-a-real-hash", "$2b$12$short"} {
		require.Errorf(t, ComparePassword(stored, "secret-pass"), "stored value %q must fail closed", stored)
	}
}
