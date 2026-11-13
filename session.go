package main

import (
	"CNKICrawlerMCP/types"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	ipLoginURL = "https://login.cnki.net/TopLoginCore/api/loginapi/IpLoginFlushPo"
	cnkiHome   = "https://www.cnki.net/"
	knsHome    = "https://kns.cnki.net/"
)

// rawJar 按域名保存 Set-Cookie 的原始值.
// Go 自带的 cookiejar 会丢掉知网的 LID (值里有 '=') 和 Ecp_LoginStuts (值是一段 JSON, 含引号).
// 下载 PDF 时 bar.cnki.net 还会在 302 里补发 SF_cookie, 下一跳必须带上, 所以不能把 Cookie 焊成一条固定字符串.
type rawJar struct {
	mu      sync.Mutex
	cookies map[string]storedCookie
}

type storedCookie struct {
	Name     string
	Value    string
	Domain   string
	HostOnly bool
	Host     string
	Path     string
	Secure   bool
	Expires  time.Time
}

func newRawJar() *rawJar {
	return &rawJar{cookies: map[string]storedCookie{}}
}

func (j *rawJar) clear() {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.cookies = map[string]storedCookie{}
}

func (j *rawJar) ingest(requestURL *url.URL, headers []string) {
	if requestURL == nil {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, h := range headers {
		c, ok := parseSetCookie(h, requestURL.Hostname())
		if !ok {
			continue
		}
		key := c.Name + "|" + c.scopeKey()
		if c.Value == "" || (!c.Expires.IsZero() && !c.Expires.After(time.Now())) {
			delete(j.cookies, key)
			continue
		}
		j.cookies[key] = c
	}
}

func (j *rawJar) headerFor(u *url.URL) string {
	if u == nil {
		return ""
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	host := u.Hostname()
	path := u.Path
	if path == "" {
		path = "/"
	}
	names := make([]string, 0, len(j.cookies))
	vals := make(map[string]string, len(j.cookies))
	for _, c := range j.cookies {
		if !c.alive() || !c.match(host, path, u.Scheme) {
			continue
		}
		if _, exists := vals[c.Name]; exists {
			continue
		}
		names = append(names, c.Name)
		vals[c.Name] = c.Value
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, name+"="+vals[name])
	}
	return strings.Join(parts, "; ")
}

func (j *rawJar) hasAlive(name string) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, c := range j.cookies {
		if c.Name == name && c.alive() && c.Value != "" {
			return true
		}
	}
	return false
}

func (j *rawJar) setManual(header string) {
	now := time.Now().Add(12 * time.Hour)
	j.mu.Lock()
	defer j.mu.Unlock()
	j.cookies = map[string]storedCookie{}
	for _, part := range strings.Split(header, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, value, ok := strings.Cut(part, "=")
		name = strings.TrimSpace(name)
		if !ok || name == "" {
			continue
		}
		c := storedCookie{
			Name:    name,
			Value:   value,
			Domain:  "cnki.net",
			Path:    "/",
			Expires: now,
		}
		j.cookies[c.Name+"|"+c.scopeKey()] = c
	}
}

func (c storedCookie) scopeKey() string {
	if c.HostOnly {
		return "host:" + c.Host
	}
	return "dom:" + c.Domain
}

func (c storedCookie) alive() bool {
	return c.Expires.IsZero() || c.Expires.After(time.Now())
}

func (c storedCookie) match(host, path, scheme string) bool {
	if c.Secure && scheme != "https" {
		return false
	}
	host = strings.ToLower(host)
	if c.HostOnly {
		if host != strings.ToLower(c.Host) {
			return false
		}
	} else if !domainMatch(host, c.Domain) {
		return false
	}
	return pathMatch(path, c.Path)
}

func domainMatch(host, domain string) bool {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	domain = strings.TrimPrefix(strings.ToLower(domain), ".")
	if host == "" || domain == "" {
		return false
	}
	if host == domain {
		return true
	}
	return strings.HasSuffix(host, "."+domain)
}

func pathMatch(reqPath, cookiePath string) bool {
	if cookiePath == "" || cookiePath == "/" {
		return true
	}
	if reqPath == cookiePath {
		return true
	}
	if strings.HasPrefix(reqPath, cookiePath) {
		if strings.HasSuffix(cookiePath, "/") {
			return true
		}
		return len(reqPath) > len(cookiePath) && reqPath[len(cookiePath)] == '/'
	}
	return false
}

func parseSetCookie(header, host string) (storedCookie, bool) {
	parts := strings.Split(header, ";")
	if len(parts) == 0 {
		return storedCookie{}, false
	}
	name, value, ok := strings.Cut(strings.TrimSpace(parts[0]), "=")
	name = strings.TrimSpace(name)
	if !ok || name == "" {
		return storedCookie{}, false
	}
	c := storedCookie{Name: name, Value: value, Path: "/", Host: host, HostOnly: true}
	for _, attr := range parts[1:] {
		attr = strings.TrimSpace(attr)
		if attr == "" {
			continue
		}
		key, val, hasVal := strings.Cut(attr, "=")
		key = strings.ToLower(strings.TrimSpace(key))
		val = strings.TrimSpace(val)
		switch key {
		case "domain":
			if hasVal && val != "" {
				c.Domain = strings.TrimPrefix(strings.ToLower(val), ".")
				c.HostOnly = false
			}
		case "path":
			if hasVal && val != "" {
				c.Path = val
			}
		case "secure":
			c.Secure = true
		case "max-age":
			if hasVal {
				if strings.HasPrefix(val, "-") {
					c.Expires = time.Unix(0, 0)
				} else if sec, err := parseInt64(val); err == nil {
					if sec <= 0 {
						c.Expires = time.Unix(0, 0)
					} else {
						c.Expires = time.Now().Add(time.Duration(sec) * time.Second)
					}
				}
			}
		case "expires":
			if hasVal && c.Expires.IsZero() {
				if t, err := time.Parse(time.RFC1123, val); err == nil {
					c.Expires = t
				} else if t, err := time.Parse(time.RFC1123Z, val); err == nil {
					c.Expires = t
				}
			}
		}
	}
	return c, true
}

func parseInt64(s string) (int64, error) {
	var n int64
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("not an int")
		}
		n = n*10 + int64(r-'0')
	}
	return n, nil
}

type cookieTransport struct {
	base http.RoundTripper
	jar  *rawJar
}

func (t *cookieTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	cloned := req.Clone(req.Context())
	if h := t.jar.headerFor(cloned.URL); h != "" {
		cloned.Header.Set("Cookie", h)
	} else {
		cloned.Header.Del("Cookie")
	}
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	resp, err := base.RoundTrip(cloned)
	if resp != nil {
		t.jar.ingest(cloned.URL, resp.Header.Values("Set-Cookie"))
	}
	return resp, err
}

var (
	cnkiJar    = newRawJar()
	cnkiBase   = newChromeTransport()
	cnkiClient = &http.Client{
		Timeout: 120 * time.Second,
		Transport: &cookieTransport{
			base: cnkiBase,
			jar:  cnkiJar,
		},
	}
	cnkiClientOnce = &http.Client{
		Timeout: 60 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Transport: &cookieTransport{
			base: cnkiBase,
			jar:  cnkiJar,
		},
	}

	loginMu          sync.Mutex
	loginInstitution string
)

type cnkiResponse struct {
	Status int
	URL    string
	Header http.Header
	Body   []byte
}

func cnkiDo(req *http.Request, follow bool) (*cnkiResponse, error) {
	return cnkiDoLimited(req, follow, 8<<20)
}

func cnkiDoLimited(req *http.Request, follow bool, limit int64) (*cnkiResponse, error) {
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", userAgent)
	}
	if req.Header.Get("Referer") == "" {
		req.Header.Set("Referer", knsHome)
	}
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8")
	}
	if req.Header.Get("Accept-Language") == "" {
		req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	}
	if req.Header.Get("Sec-Ch-Ua") == "" {
		req.Header.Set("Sec-Ch-Ua", `"Google Chrome";v="131", "Chromium";v="131", "Not_A Brand";v="24"`)
		req.Header.Set("Sec-Ch-Ua-Mobile", "?0")
		req.Header.Set("Sec-Ch-Ua-Platform", `"Windows"`)
	}
	cnkiThrottle.waitTurn()
	client := cnkiClient
	if !follow {
		client = cnkiClientOnce
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("响应超过 %d 字节, 已中止", limit)
	}
	final := ""
	if resp.Request != nil && resp.Request.URL != nil {
		final = resp.Request.URL.String()
	}
	out := &cnkiResponse{Status: resp.StatusCode, URL: final, Header: resp.Header.Clone(), Body: body}
	if !follow {
		return out, nil
	}
	return absorbVerify(req, out, limit)
}

func cnkiGet(rawURL, referer string) (*cnkiResponse, error) {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	if referer != "" {
		req.Header.Set("Referer", referer)
	}
	return cnkiDo(req, true)
}

func cnkiPostForm(rawURL string, form url.Values, referer string) (*cnkiResponse, error) {
	req, err := http.NewRequest(http.MethodPost, rawURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
	req.Header.Set("Origin", "https://kns.cnki.net")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	if referer != "" {
		req.Header.Set("Referer", referer)
	}
	return cnkiDo(req, true)
}

func loggedInLocked() bool {
	return cnkiJar.hasAlive("LID") || cnkiJar.hasAlive("Ecp_LoginStuts")
}

// CheckLogin 有未过期的校园网会话就直接用, 否则重新做 IP 登录.
func CheckLogin() (bool, error) {
	loginMu.Lock()
	defer loginMu.Unlock()
	if loggedInLocked() {
		return true, nil
	}
	return refreshLocked()
}

// RefreshLogin 清掉旧会话并重新 IP 登录.
func RefreshLogin() (bool, error) {
	loginMu.Lock()
	defer loginMu.Unlock()
	return refreshLocked()
}

func refreshLocked() (bool, error) {
	cnkiJar.clear()
	loginInstitution = ""
	cookie, err := ipLogin()
	if err != nil {
		return false, err
	}
	if cookie == "" || !loggedInLocked() {
		return false, fmt.Errorf("IP 登录未拿到可用会话, 请确认当前电脑连着校园网. 不需要个人账号, 也不需要再打开机构登录页")
	}
	return true, nil
}

func ipLogin() (string, error) {
	req, err := http.NewRequest(http.MethodPost, ipLoginURL, bytes.NewReader([]byte("{}")))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://www.cnki.net")
	req.Header.Set("Referer", cnkiHome)
	resp, err := cnkiDo(req, true)
	if err != nil {
		return "", fmt.Errorf("IP 登录请求失败: %w", err)
	}
	payload := unwrapJSONP(string(resp.Body))
	var login types.LoginResponse
	if err := json.Unmarshal([]byte(payload), &login); err != nil {
		return "", fmt.Errorf("IP 登录响应无法解析 (HTTP %d)", resp.Status)
	}
	if !login.IsSuccess {
		msg := strings.TrimSpace(login.ErrorMsg)
		if msg == "" {
			msg = strings.TrimSpace(login.Msg)
		}
		if msg == "" {
			msg = "知网拒绝了当前 IP"
		}
		return "", fmt.Errorf("IP 登录失败: %s. 请连接校园网后再试, 不需要个人账号", msg)
	}
	loginInstitution = login.ShowName
	if !loggedInLocked() {
		return "", fmt.Errorf("IP 登录返回成功, 但没有 LID 会话, 请确认当前电脑连着校园网")
	}
	return GetGlobalCookie(), nil
}

func unwrapJSONP(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '{'); i >= 0 {
		if j := strings.LastIndexByte(s, '}'); j > i {
			return s[i : j+1]
		}
	}
	return s
}

// SetGlobalCookie 仅在用户明确给出浏览器 Cookie 时使用. 自动下载不依赖它.
func SetGlobalCookie(cookie string) {
	loginMu.Lock()
	defer loginMu.Unlock()
	cnkiJar.setManual(cookie)
}

// GetGlobalCookie 返回当前会发给 kns.cnki.net 的 Cookie, 供调试. 下载不要拿这个字符串自己去请求.
func GetGlobalCookie() string {
	u, err := url.Parse(knsHome)
	if err != nil {
		return ""
	}
	return cnkiJar.headerFor(u)
}

// LoginInstitution 是最近一次 IP 登录识别到的机构名, 例如学校名.
func LoginInstitution() string {
	loginMu.Lock()
	defer loginMu.Unlock()
	return loginInstitution
}

// IpLogin 保留给调用方: 登录并把原始 Cookie 放进会话.
func IpLogin() (string, error) {
	loginMu.Lock()
	defer loginMu.Unlock()
	cnkiJar.clear()
	return ipLogin()
}
