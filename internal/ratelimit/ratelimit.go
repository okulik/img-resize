// Package ratelimit throttles clients with counters shared through Redis.
package ratelimit

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"time"

	redis "github.com/redis/go-redis/v9"
)

const counterTTL = 2 * time.Minute

// Limiter counts amounts per key in fixed one-minute windows. Machines sharing
// a Redis share the counts.
type Limiter struct {
	client *redis.Client
	now    func() time.Time
}

func New(client *redis.Client, now func() time.Time) *Limiter {
	return &Limiter{client: client, now: now}
}

// Allow adds amount to the count of key in bucket for the current minute and
// reports whether the count is still within limit. When it is not, retryAfter
// is the time left until the next minute.
func (l *Limiter) Allow(ctx context.Context, bucket, key string, amount, limit int64) (allowed bool, retryAfter time.Duration, err error) {
	now := l.now()
	redisKey := fmt.Sprintf("resize:%s:%s:%d", bucket, key, now.Unix()/60)

	pipe := l.client.TxPipeline()
	count := pipe.IncrBy(ctx, redisKey, amount)
	pipe.Expire(ctx, redisKey, counterTTL)
	if _, err := pipe.Exec(ctx); err != nil {
		return false, 0, err
	}

	retryAfter = now.Truncate(time.Minute).Add(time.Minute).Sub(now)

	return count.Val() <= limit, retryAfter, nil
}

// ClientIP returns the address of the client that sent r. Fly-Client-IP is used
// only when trustFly is set, as any client can send it otherwise, and only when
// it holds an address.
func ClientIP(r *http.Request, trustFly bool) string {
	if trustFly {
		ip := r.Header.Get("Fly-Client-IP")
		if _, err := netip.ParseAddr(ip); err == nil {
			return ip
		}
	}

	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}

	return host
}

// PrefixKey names the network that ip belongs to, using ipv4Bits or ipv6Bits as
// the prefix length. IPv4-mapped IPv6 addresses count as IPv4. A length out of
// range for the address family means the whole address. Text that is not an IP
// address is returned as is.
func PrefixKey(ip string, ipv4Bits, ipv6Bits int) string {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return ip
	}

	addr = addr.Unmap().WithZone("")
	bits := ipv6Bits
	if addr.Is4() {
		bits = ipv4Bits
	}
	if bits < 0 || bits > addr.BitLen() {
		bits = addr.BitLen()
	}

	return netip.PrefixFrom(addr, bits).Masked().String()
}
