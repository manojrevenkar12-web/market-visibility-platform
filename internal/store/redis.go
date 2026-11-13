package store

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/manojrevenkar12-web/market-visibility-platform/internal/visibility"
)

// Redis is a minimal RESP2 client: enough for pipelined MSET/MGET of visibility masks. One connection, guarded by a
// mutex; the feed service runs one per worker.
type Redis struct {
	mu   sync.Mutex
	conn net.Conn
	r    *bufio.Reader
	TTL  time.Duration
}

func DialRedis(ctx context.Context, addr string) (*Redis, error) {
	var d net.Dialer
	c, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	return &Redis{conn: c, r: bufio.NewReader(c)}, nil
}

func (r *Redis) Close() error { return r.conn.Close() }

func encode(args ...string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "*%d\r\n", len(args))
	for _, a := range args {
		fmt.Fprintf(&b, "$%d\r\n%s\r\n", len(a), a)
	}
	return []byte(b.String())
}

// reply reads one RESP value: string, int64, nil, []any, or an error reply as error.
func (r *Redis) reply() (any, error) {
	line, err := r.r.ReadString('\n')
	if err != nil {
		return nil, err
	}
	line = strings.TrimSuffix(line, "\r\n")
	if line == "" {
		return nil, errors.New("redis: empty reply")
	}
	switch line[0] {
	case '+':
		return line[1:], nil
	case '-':
		return nil, errors.New("redis: " + line[1:])
	case ':':
		return strconv.ParseInt(line[1:], 10, 64)
	case '$':
		n, err := strconv.Atoi(line[1:])
		if err != nil || n < 0 {
			return nil, err
		}
		buf := make([]byte, n+2)
		if _, err := io.ReadFull(r.r, buf); err != nil {
			return nil, err
		}
		return string(buf[:n]), nil
	case '*':
		n, err := strconv.Atoi(line[1:])
		if err != nil || n < 0 {
			return nil, err
		}
		out := make([]any, n)
		for i := range out {
			if out[i], err = r.reply(); err != nil {
				return nil, err
			}
		}
		return out, nil
	}
	return nil, fmt.Errorf("redis: unexpected reply %q", line)
}

// Pipeline sends commands in one write and reads all replies.
func (r *Redis) Pipeline(ctx context.Context, cmds [][]string) ([]any, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if dl, ok := ctx.Deadline(); ok {
		_ = r.conn.SetDeadline(dl)
		defer func() { _ = r.conn.SetDeadline(time.Time{}) }()
	}
	var buf []byte
	for _, c := range cmds {
		buf = append(buf, encode(c...)...)
	}
	if _, err := r.conn.Write(buf); err != nil {
		return nil, err
	}
	out := make([]any, len(cmds))
	var firstErr error
	for i := range cmds {
		v, err := r.reply()
		if err != nil && firstErr == nil {
			firstErr = err
		}
		out[i] = v
	}
	return out, firstErr
}

func key(id uint64) string { return "vis:" + strconv.FormatUint(id, 10) }

func formatMasks(m visibility.Masks) string {
	return strconv.FormatUint(m[0], 16) + "," + strconv.FormatUint(m[1], 16) + "," + strconv.FormatUint(m[2], 16)
}

func parseMasks(s string) (visibility.Masks, error) {
	var m visibility.Masks
	parts := strings.Split(s, ",")
	if len(parts) != len(m) {
		return m, fmt.Errorf("bad masks %q", s)
	}
	for i, p := range parts {
		v, err := strconv.ParseUint(p, 16, 64)
		if err != nil {
			return m, err
		}
		m[i] = v
	}
	return m, nil
}

// SetMasks writes masks in one round trip.
func (r *Redis) SetMasks(ctx context.Context, masks map[uint64]visibility.Masks) error {
	if len(masks) == 0 {
		return nil
	}
	args := []string{"MSET"}
	for id, m := range masks {
		args = append(args, key(id), formatMasks(m))
	}
	cmds := [][]string{args}
	if r.TTL > 0 {
		for id := range masks {
			cmds = append(cmds, []string{"PEXPIRE", key(id), strconv.FormatInt(r.TTL.Milliseconds(), 10)})
		}
	}
	_, err := r.Pipeline(ctx, cmds)
	return err
}

// GetMasks reads masks in one round trip. Missing listings are absent from the result.
func (r *Redis) GetMasks(ctx context.Context, ids []uint64) (map[uint64]visibility.Masks, error) {
	out := map[uint64]visibility.Masks{}
	if len(ids) == 0 {
		return out, nil
	}
	args := []string{"MGET"}
	for _, id := range ids {
		args = append(args, key(id))
	}
	res, err := r.Pipeline(ctx, [][]string{args})
	if err != nil {
		return nil, err
	}
	vals, ok := res[0].([]any)
	if !ok {
		return nil, errors.New("redis: MGET did not return an array")
	}
	for i, v := range vals {
		s, ok := v.(string)
		if !ok {
			continue
		}
		m, err := parseMasks(s)
		if err != nil {
			return nil, err
		}
		out[ids[i]] = m
	}
	return out, nil
}
