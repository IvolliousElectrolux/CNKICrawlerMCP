package main

import (
	"errors"
	"math/rand"
	"sync"
	"time"
)

const userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/114.0.0.0 Safari/537.36"

// ErrCookieExpired 表示 CNKI 返回了登录页，说明全局 Cookie 已失效，需要重新登录
var ErrCookieExpired = errors.New("CNKI Cookie 已失效，需要重新登录")

// ErrBlocked 表示触发了 CNKI 的风控/人机验证
var ErrBlocked = errors.New("触发 CNKI 人机验证或访问受限，请降低检索频率后重试")

// ---------- 请求节流 ----------
//
// 原先每个请求之间固定 sleep 100ms，且对每篇论文都单独抓一次影响因子，
// 一次检索会产生几十次请求，极易触发风控。这里改为全局最小间隔 + 随机抖动。

type throttle struct {
	mu     sync.Mutex
	last   time.Time
	minGap time.Duration
	jitter time.Duration
}

var cnkiThrottle = &throttle{
	minGap: 1200 * time.Millisecond,
	jitter: 900 * time.Millisecond,
}

// waitTurn 阻塞直到距离上一次请求已过去 minGap + 随机抖动
func (t *throttle) waitTurn() {
	t.mu.Lock()
	gap := t.minGap
	if t.jitter > 0 {
		gap += time.Duration(rand.Int63n(int64(t.jitter)))
	}
	earliest := t.last.Add(gap)
	now := time.Now()
	if now.Before(earliest) {
		d := earliest.Sub(now)
		t.mu.Unlock()
		time.Sleep(d)
		t.mu.Lock()
	}
	t.last = time.Now()
	t.mu.Unlock()
}

// ---------- 影响因子缓存 ----------
//
// 一次检索返回的几十篇论文往往来自同一个期刊，原先每篇都发一次请求，
// 去重 + 缓存后请求数可从几十次降到个位数。

type factorEntry struct {
	Composite     float64
	Comprehensive float64
	Failed        bool
	Expire        time.Time
}

var (
	factorMu    sync.RWMutex
	factorCache = map[string]factorEntry{}
)

const (
	// factorTTL 成功结果的有效期：影响因子短期内不会变
	factorTTL = 24 * time.Hour
	// factorFailTTL 失败结果的负缓存有效期：既避免每次检索都白跑一遍，
	// 又能让瞬时限流在短时间内自动恢复
	factorFailTTL = 10 * time.Minute
)

func getCachedFactor(link string) (factorEntry, bool) {
	factorMu.RLock()
	defer factorMu.RUnlock()
	e, ok := factorCache[link]
	if !ok || time.Now().After(e.Expire) {
		return factorEntry{}, false
	}
	return e, true
}

func putCachedFactor(link string, composite, comprehensive float64) {
	factorMu.Lock()
	defer factorMu.Unlock()
	factorCache[link] = factorEntry{
		Composite:     composite,
		Comprehensive: comprehensive,
		Expire:        time.Now().Add(factorTTL),
	}
}

// putFailedFactor 记录一次解析失败，短期内不再重试该期刊
func putFailedFactor(link string) {
	factorMu.Lock()
	defer factorMu.Unlock()
	factorCache[link] = factorEntry{
		Failed: true,
		Expire: time.Now().Add(factorFailTTL),
	}
}

// ---------- 重试 ----------

// withRetry 对可恢复的错误做指数退避重试；ErrBlocked / ErrCookieExpired 不重试
func withRetry(attempts int, fn func() error) error {
	if attempts < 1 {
		attempts = 1
	}
	var err error
	for i := 0; i < attempts; i++ {
		if err = fn(); err == nil {
			return nil
		}
		if errors.Is(err, ErrCookieExpired) || errors.Is(err, ErrBlocked) {
			return err
		}
		if i < attempts-1 {
			backoff := time.Duration(1<<uint(i)) * time.Second
			backoff += time.Duration(rand.Int63n(int64(500 * time.Millisecond)))
			time.Sleep(backoff)
		}
	}
	return err
}
