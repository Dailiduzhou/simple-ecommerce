package pwdhash

import (
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

// Cost is the bcrypt work factor used for every password hash this service
// writes. Every extra unit doubles the work an offline attacker must do per
// guess, and 12 keeps a single verification within an interactive login
// latency budget (~250ms on current server hardware).
//
// The project has no legacy password rows, so nothing needs to stay backward
// compatible with bcrypt.DefaultCost (10): hashing always uses Cost and
// verification refuses any other cost, which fails closed on a hash that was
// imported or written outside this package instead of silently accepting
// weaker work.
//
// Raising Cost later means every stored hash must be rehashed first (or the
// check below must be relaxed to accept the previous cost): the equality test
// is deliberately not a minimum.
const Cost = 12

func HashPassword(password string) (string, error) {
	hashedPasswordBytes, err := bcrypt.GenerateFromPassword([]byte(password), Cost)
	if err != nil {
		return "", err
	}
	return string(hashedPasswordBytes), nil
}

// ComparePassword verifies password against the stored hash. A hash whose cost
// is not the current Cost is rejected before any comparison: this service never
// produces one.
func ComparePassword(dbPassword, password string) error {
	cost, err := bcrypt.Cost([]byte(dbPassword))
	if err != nil {
		return err
	}
	if cost != Cost {
		return fmt.Errorf("unsupported bcrypt cost %d: hashes must be created with cost %d", cost, Cost)
	}
	return bcrypt.CompareHashAndPassword([]byte(dbPassword), []byte(password))
}
