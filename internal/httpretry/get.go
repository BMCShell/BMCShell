// Package httpretry retries read-only HTTP downloads, including failed body reads.
package httpretry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	attempts      = 3
	maxRetryDelay = 30 * time.Second
)

type requestFailure struct {
	message string
	cause   error
}

func (e requestFailure) Error() string { return e.message }
func (e requestFailure) Unwrap() error { return e.cause }

type retryFailure struct {
	error
	after time.Duration
}

func (e retryFailure) Unwrap() error { return e.error }

// Get fetches a GET request and calls consume with its response body. The
// callback must discard partial output before returning a body read error.
func Get(client *http.Client, request *http.Request, limit int64, progress io.Writer, label string, consume func(io.Reader) error) error {
	ctx := request.Context()
	for attempt := 1; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return networkFailure(err)
		}
		err := getOnce(client, request.Clone(ctx), limit, consume)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return networkFailure(ctx.Err())
		}
		var failure retryFailure
		if !errors.As(err, &failure) || failure.after > maxRetryDelay {
			return err
		}
		if attempt == attempts {
			return fmt.Errorf("重试 %d 次后仍失败：%w", attempt-1, err)
		}
		delay := max(time.Duration(attempt)*time.Second, failure.after)
		if progress != nil {
			fmt.Fprintf(progress, "%s：%v，等待约 %d 秒后重试（第 %d/%d 次请求）…\n", label, err, (delay+time.Second-1)/time.Second, attempt+1, attempts)
		}
		if err := wait(ctx, delay); err != nil {
			return networkFailure(err)
		}
	}
}

func getOnce(client *http.Client, request *http.Request, limit int64, consume func(io.Reader) error) error {
	response, err := client.Do(request)
	if err != nil {
		return networkFailure(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		err := fmt.Errorf("服务器返回 HTTP %d", response.StatusCode)
		switch response.StatusCode {
		case http.StatusRequestTimeout, http.StatusTooManyRequests, http.StatusInternalServerError,
			http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			return retryFailure{err, retryAfter(response.Header.Get("Retry-After"))}
		default:
			return err
		}
	}
	if response.ContentLength > limit {
		return errors.New("网络响应超过大小限制")
	}
	return consume(responseReader{response.Body})
}

type responseReader struct{ io.Reader }

func (r responseReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if err != nil && err != io.EOF {
		err = networkFailure(err)
	}
	return n, err
}

func networkFailure(err error) error {
	// A transport error can contain a URL with signed query parameters.
	var transport net.Error
	switch {
	case errors.Is(err, context.Canceled):
		return requestFailure{"网络请求已取消", context.Canceled}
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &transport) && transport.Timeout():
		return retryFailure{requestFailure{"网络请求超时", context.DeadlineExceeded}, 0}
	case errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, io.EOF), transientConnection(err):
		return retryFailure{errors.New("网络连接中断或暂时不可用"), 0}
	default:
		return errors.New("网络请求失败，请检查网络、代理或服务器配置")
	}
}

func transientConnection(err error) bool {
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return dns.IsTemporary
	}
	var op *net.OpError
	for errors.As(err, &op) {
		switch op.Op {
		case "dial", "read", "write":
			return true
		}
		err = op.Err
	}
	return false
}

func retryAfter(value string) time.Duration {
	value = strings.TrimSpace(value)
	if seconds, err := strconv.ParseUint(value, 10, 64); err == nil {
		if seconds > uint64(maxRetryDelay/time.Second) {
			return maxRetryDelay + time.Second
		}
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(value); err == nil {
		return time.Until(when)
	}
	return 0
}

func wait(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
