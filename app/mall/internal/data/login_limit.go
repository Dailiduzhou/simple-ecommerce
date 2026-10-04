package data

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Redis time and one atomic reservation bound concurrent guesses across every
// instance/IP, including requests whose bcrypt check has not finished yet.
// The stored value is the time when the entire burst will be replenished.
// Rejections never write or extend the TTL. Keys contain only phone HMACs.
var reserveLoginAttemptScript = redis.NewScript(`
local clock = redis.call('TIME')
local now = tonumber(clock[1]) * 1000 + math.floor(tonumber(clock[2]) / 1000)
local interval = tonumber(ARGV[1])
local burst = tonumber(ARGV[2])
local ready = math.max(now, tonumber(redis.call('GET', KEYS[1])) or now)
local wait = ready - (burst - 1) * interval - now
if wait > 0 then return wait end
ready = ready + interval
redis.call('SET', KEYS[1], string.format('%.0f', ready), 'PX', ready - now)
return 0`)

func (r *AuthRepo) ReserveLoginAttempt(ctx context.Context, phoneHash string, burst int64, interval time.Duration) (time.Duration, error) {
	if phoneHash == "" || burst < 1 || burst > 100 || interval < time.Second || interval > time.Minute {
		return 0, fmt.Errorf("invalid account login quota")
	}
	wait, err := reserveLoginAttemptScript.Run(ctx, r.rdb, []string{redisKey("auth", "login", "budget", phoneHash)}, interval.Milliseconds(), burst).Int64()
	if err != nil {
		return 0, err
	}
	return time.Duration(wait) * time.Millisecond, nil
}
