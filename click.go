package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/aes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	verifyGetURL   = "https://kns.cnki.net/verify-api/get"
	verifyCheckURL = "https://kns.cnki.net/verify-api/web/check"
	clickAttempts  = 3
)

// captchaDepthKey 避免验证接口自己再走进点选, 也限制原请求的重放次数.
type captchaDepthKey struct{}

func captchaDepth(ctx context.Context) int {
	if ctx == nil {
		return 0
	}
	n, _ := ctx.Value(captchaDepthKey{}).(int)
	return n
}

type clickPoint struct {
	X int `json:"x"`
	Y int `json:"y"`
}

type clickReply struct {
	Ready  bool         `json:"ready"`
	Points []clickPoint `json:"points"`
	Error  string       `json:"error"`
}

type clickProc struct {
	mu  sync.Mutex
	cmd *exec.Cmd
	in  io.WriteCloser
	out *bufio.Reader
}

var (
	cnkiClientUID = newClientUID()
	clicks        clickProc
)

func newClientUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "3f1c0b7a-6d2e-4a18-9c55-0e7b1a2d4f60"
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// absorbVerify 跟随后如果落在依次点击的验证页, 就识别并提交, 再打开通过后的地址.
func absorbVerify(req *http.Request, resp *cnkiResponse, limit int64) (*cnkiResponse, error) {
	if resp == nil || !isVerifyResponse(resp) {
		return resp, nil
	}
	if captchaDepth(req.Context()) >= 2 {
		return nil, ErrBlocked
	}
	page := verifyPageURL(resp)
	if page == "" {
		return nil, fmt.Errorf("知网返回了安全验证, 但页面上没有 captcha 参数")
	}
	openURL, captchaID, err := passClickWord(req.Context(), page)
	if err != nil {
		return nil, err
	}
	ctx := context.WithValue(req.Context(), captchaDepthKey{}, captchaDepth(req.Context())+1)
	if req.Method == http.MethodGet {
		dest := preferOpen(openURL, req.URL.String(), captchaID)
		nxt, err := http.NewRequestWithContext(ctx, http.MethodGet, dest, nil)
		if err != nil {
			return nil, err
		}
		copyReferer(nxt, req)
		return cnkiDoLimited(nxt, true, limit)
	}
	if openURL != "" {
		warm, err := http.NewRequestWithContext(ctx, http.MethodGet, openURL, nil)
		if err == nil {
			copyReferer(warm, req)
			_, _ = cnkiDoLimited(warm, true, 1<<20)
		}
	}
	cloned, err := replayRequest(req, ctx)
	if err != nil {
		return nil, err
	}
	return cnkiDoLimited(cloned, true, limit)
}

func isVerifyResponse(resp *cnkiResponse) bool {
	if strings.Contains(resp.URL, "/verify/home") || strings.Contains(resp.Header.Get("Location"), "/verify/home") {
		return true
	}
	return strings.Contains(string(resp.Body), "<title>安全验证</title>")
}

func verifyPageURL(resp *cnkiResponse) string {
	if strings.Contains(resp.URL, "/verify/home") || strings.Contains(resp.URL, "captchaType=") {
		return resp.URL
	}
	loc := resp.Header.Get("Location")
	if strings.Contains(loc, "/verify/home") || strings.Contains(loc, "captchaType=") {
		return loc
	}
	return ""
}

func copyReferer(dst, src *http.Request) {
	if ref := src.Header.Get("Referer"); ref != "" {
		dst.Header.Set("Referer", ref)
	}
}

func preferOpen(openURL, original, captchaID string) string {
	if strings.HasPrefix(openURL, "http://") || strings.HasPrefix(openURL, "https://") {
		return openURL
	}
	return appendCaptchaID(original, captchaID)
}

func appendCaptchaID(raw, id string) string {
	if id == "" || strings.Contains(raw, "captchaId=") {
		return raw
	}
	if strings.Contains(raw, "?") {
		return raw + "&captchaId=" + id
	}
	return raw + "?captchaId=" + id
}

func replayRequest(req *http.Request, ctx context.Context) (*http.Request, error) {
	clone := req.Clone(ctx)
	if req.Body == nil || req.Body == http.NoBody {
		clone.Body = nil
		return clone, nil
	}
	if req.GetBody == nil {
		return nil, fmt.Errorf("无法在通过验证后重放这个请求")
	}
	body, err := req.GetBody()
	if err != nil {
		return nil, err
	}
	clone.Body = body
	return clone, nil
}

type verifyGetReq struct {
	CaptchaType string `json:"captchaType"`
	ClientUID   string `json:"clientUid"`
	Ident       string `json:"ident"`
	CaptchaID   string `json:"captchaId"`
	TS          int64  `json:"ts"`
}

type verifyCheckReq struct {
	CaptchaType string `json:"captchaType"`
	PointJSON   string `json:"pointJson"`
	Token       string `json:"token"`
	Ident       string `json:"ident"`
	ReturnURL   string `json:"returnUrl"`
}

type verifyData struct {
	OriginalImageBase64 string   `json:"originalImageBase64"`
	WordList            []string `json:"wordList"`
	Token               string   `json:"token"`
	SecretKey           string   `json:"secretKey"`
	ReturnURL           string   `json:"returnUrl"`
}

type verifyResp struct {
	Code    json.RawMessage `json:"code"`
	Message string          `json:"message"`
	Data    *verifyData     `json:"data"`
}

func (r verifyResp) ok() bool {
	return strings.Trim(string(r.Code), `"`) == "0"
}

func passClickWord(parent context.Context, page string) (string, string, error) {
	captchaType, ident, captchaID, returnURL, err := parseVerifyPage(page)
	if err != nil {
		return "", "", err
	}
	if captchaType != "" && captchaType != "clickWord" {
		return "", "", fmt.Errorf("遇到的是 %s 验证, 目前只自动通过依次点击文字", captchaType)
	}
	if captchaType == "" {
		captchaType = "clickWord"
	}
	var last error
	for attempt := 1; attempt <= clickAttempts; attempt++ {
		openURL, err := clickOnce(parent, page, captchaType, ident, captchaID, returnURL)
		if err == nil {
			return openURL, captchaID, nil
		}
		last = err
		log.Printf("点选验证第 %d 次未通过: %v", attempt, err)
	}
	return "", captchaID, fmt.Errorf("点选验证未通过: %w", last)
}

func clickOnce(parent context.Context, page, captchaType, ident, captchaID, returnURL string) (string, error) {
	got, err := cnkiPostJSON(parent, verifyGetURL, verifyGetReq{
		CaptchaType: captchaType,
		ClientUID:   cnkiClientUID,
		Ident:       ident,
		CaptchaID:   captchaID,
		TS:          time.Now().UnixMilli(),
	}, page)
	if err != nil {
		return "", err
	}
	var resp verifyResp
	if err := json.Unmarshal(got.Body, &resp); err != nil {
		return "", fmt.Errorf("验证码响应无法解析 (HTTP %d)", got.Status)
	}
	if !resp.ok() || resp.Data == nil {
		msg := strings.TrimSpace(resp.Message)
		if msg == "" {
			msg = "知网没有返回验证码图片"
		}
		return "", fmt.Errorf("%s", msg)
	}
	words := make([]string, 0, len(resp.Data.WordList))
	for _, word := range resp.Data.WordList {
		word = strings.TrimSpace(word)
		if word != "" {
			words = append(words, word)
		}
	}
	if len(words) == 0 {
		return "", fmt.Errorf("验证码没有给出要点的字")
	}
	img, err := decodeImage(resp.Data.OriginalImageBase64)
	if err != nil {
		return "", err
	}
	log.Printf("知网点选验证, 依次点击 %s", strings.Join(words, " "))
	points, err := clicks.points(img, words)
	if err != nil {
		return "", err
	}
	plain, err := json.Marshal(points)
	if err != nil {
		return "", err
	}
	pointJSON, err := encryptPoint(string(plain), resp.Data.SecretKey)
	if err != nil {
		return "", err
	}
	checked, err := cnkiPostJSON(parent, verifyCheckURL, verifyCheckReq{
		CaptchaType: captchaType,
		PointJSON:   pointJSON,
		Token:       resp.Data.Token,
		Ident:       ident,
		ReturnURL:   returnURL,
	}, page)
	if err != nil {
		return "", err
	}
	var check verifyResp
	if err := json.Unmarshal(checked.Body, &check); err != nil {
		return "", fmt.Errorf("验证结果无法解析 (HTTP %d)", checked.Status)
	}
	if !check.ok() {
		msg := strings.TrimSpace(check.Message)
		if msg == "" {
			msg = "坐标未被接受"
		}
		return "", fmt.Errorf("%s", msg)
	}
	ret := ""
	if check.Data != nil {
		ret = strings.TrimSpace(check.Data.ReturnURL)
	}
	if ret != "" && !strings.Contains(ret, "captchaId=") {
		if strings.Contains(ret, "?") {
			ret += "&captchaId=" + captchaID
		} else {
			ret += "?captchaId=" + captchaID
		}
	}
	log.Printf("点选验证已通过")
	return ret, nil
}

func parseVerifyPage(raw string) (captchaType, ident, captchaID, returnURL string, err error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", "", "", err
	}
	captchaType = queryRaw(u, "captchaType")
	if decoded, decErr := url.QueryUnescape(captchaType); decErr == nil {
		captchaType = decoded
	}
	ident = queryRaw(u, "ident")
	captchaID = queryRaw(u, "captchaId")
	returnURL = queryRaw(u, "returnUrl")
	if ident == "" || captchaID == "" {
		return "", "", "", "", fmt.Errorf("验证页缺少 ident 或 captchaId")
	}
	return captchaType, ident, captchaID, returnURL, nil
}

func queryRaw(u *url.URL, key string) string {
	for _, part := range strings.Split(u.RawQuery, "&") {
		name, value, ok := strings.Cut(part, "=")
		if ok && name == key {
			return value
		}
	}
	return ""
}

func decodeImage(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, ","); i >= 0 && strings.Contains(strings.ToLower(s[:i]), "base64") {
		s = s[i+1:]
	}
	s = strings.Map(func(r rune) rune {
		switch r {
		case '\n', '\r', ' ', '\t':
			return -1
		default:
			return r
		}
	}, s)
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		raw, err = base64.RawStdEncoding.DecodeString(s)
	}
	if err != nil || len(raw) == 0 {
		return nil, fmt.Errorf("验证码图片无法解码")
	}
	return raw, nil
}

func encryptPoint(plain, key string) (string, error) {
	if key == "" {
		return plain, nil
	}
	block, err := aes.NewCipher([]byte(key))
	if err != nil {
		return "", fmt.Errorf("secretKey 不能作为 AES 密钥: %w", err)
	}
	src := pkcs7Pad([]byte(plain), block.BlockSize())
	dst := make([]byte, len(src))
	for i := 0; i < len(src); i += block.BlockSize() {
		block.Encrypt(dst[i:i+block.BlockSize()], src[i:i+block.BlockSize()])
	}
	return base64.StdEncoding.EncodeToString(dst), nil
}

func pkcs7Pad(src []byte, n int) []byte {
	pad := n - len(src)%n
	out := make([]byte, len(src)+pad)
	copy(out, src)
	for i := len(src); i < len(out); i++ {
		out[i] = byte(pad)
	}
	return out
}

func cnkiPostJSON(parent context.Context, rawURL string, payload any, referer string) (*cnkiResponse, error) {
	if parent == nil {
		parent = context.Background()
	}
	buf := &bytes.Buffer{}
	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(payload); err != nil {
		return nil, err
	}
	body := bytes.TrimRight(buf.Bytes(), "\n")
	ctx := context.WithValue(parent, captchaDepthKey{}, 99)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json;charset=utf-8")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("uniplatform", "NZKPT")
	req.Header.Set("Origin", "https://kns.cnki.net")
	if referer != "" {
		req.Header.Set("Referer", referer)
	}
	return cnkiDo(req, true)
}

func (p *clickProc) points(img []byte, words []string) ([]clickPoint, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.ensure(); err != nil {
		return nil, err
	}
	job, err := json.Marshal(struct {
		Image string   `json:"image"`
		Words []string `json:"words"`
	}{base64.StdEncoding.EncodeToString(img), words})
	if err != nil {
		return nil, err
	}
	if _, err = p.in.Write(append(job, '\n')); err != nil {
		p.stop()
		return nil, err
	}
	line, err := readLine(p.out, 90*time.Second)
	if err != nil {
		p.stop()
		return nil, err
	}
	var reply clickReply
	if err := json.Unmarshal([]byte(line), &reply); err != nil {
		p.stop()
		return nil, fmt.Errorf("点选识别返回无法解析: %w", err)
	}
	if reply.Error != "" {
		return nil, fmt.Errorf("%s", reply.Error)
	}
	if len(reply.Points) != len(words) {
		return nil, fmt.Errorf("识别到 %d 个点, 需要 %d 个", len(reply.Points), len(words))
	}
	return reply.Points, nil
}

func (p *clickProc) ensure() error {
	if p.cmd != nil && p.cmd.Process != nil && p.cmd.ProcessState == nil {
		return nil
	}
	p.stop()
	path, err := clickHelperPath()
	if err != nil {
		return err
	}
	log.Printf("加载点选识别: %s", path)
	cmd := exec.Command(path)
	hideHelperWindow(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动点选识别失败: %w", err)
	}
	go logHelper(stderr)
	p.cmd = cmd
	p.in = stdin
	p.out = bufio.NewReaderSize(stdout, 1<<20)
	line, err := readLine(p.out, 120*time.Second)
	if err != nil {
		p.stop()
		return fmt.Errorf("点选识别没有就绪: %w", err)
	}
	var reply clickReply
	if err := json.Unmarshal([]byte(line), &reply); err != nil {
		p.stop()
		return fmt.Errorf("点选识别没有就绪: %s", strings.TrimSpace(line))
	}
	if reply.Error != "" {
		p.stop()
		return fmt.Errorf("点选识别没有就绪: %s", reply.Error)
	}
	if !reply.Ready {
		p.stop()
		return fmt.Errorf("点选识别没有就绪: %s", strings.TrimSpace(line))
	}
	return nil
}

func (p *clickProc) stop() {
	cmd := p.cmd
	in := p.in
	p.cmd = nil
	p.in = nil
	p.out = nil
	if in != nil {
		_ = in.Close()
	}
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}
}

func clickHelperPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	name := "cnki-click"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(filepath.Dir(exe), name)
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("缺少点选识别程序 %s, 请把 Actions 产物里的 %s 和主程序放在同一目录", path, name)
	}
	return path, nil
}

func readLine(r *bufio.Reader, d time.Duration) (string, error) {
	type got struct {
		s   string
		err error
	}
	ch := make(chan got, 1)
	go func() {
		s, err := r.ReadString('\n')
		ch <- got{s, err}
	}()
	select {
	case v := <-ch:
		if v.err != nil {
			return "", v.err
		}
		return v.s, nil
	case <-time.After(d):
		return "", fmt.Errorf("超时")
	}
}

func logHelper(r io.Reader) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		log.Printf("cnki-click: %s", sc.Text())
	}
}
