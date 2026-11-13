package main

import (
	"CNKICrawlerMCP/types"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

// ErrNotSubscribed 表示校园网机构没有订购这篇文献, 重试登录也下不下来.
var ErrNotSubscribed = errors.New("当前校园网机构未订购该文献, 无法下载 PDF")

type downloadPaperArgs struct {
	Href    string `json:"href"`
	SaveDir string `json:"saveDir"`
}

type downloadResult struct {
	Title       string   `json:"title"`
	Authors     []string `json:"authors"`
	Keywords    []string `json:"keywords"`
	Abstract    string   `json:"abstract"`
	Year        int      `json:"year"`
	OnlineFirst bool     `json:"onlineFirst"`
	Institution string   `json:"institution,omitempty"`
	File        string   `json:"file,omitempty"`
	Bytes       int      `json:"bytes,omitempty"`
	Format      string   `json:"format,omitempty"`
}

func handleDownloadPaper(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if ok, err := CheckLogin(); !ok || err != nil {
		return toolError(fmt.Sprintf("[ERROR] 登录失败: %v", err)), nil
	}
	args, err := ParseArgs[downloadPaperArgs](request.Params.Arguments)
	if err != nil {
		return toolError(fmt.Sprintf("[ERROR] 参数错误: %v", err)), nil
	}
	if strings.TrimSpace(args.Href) == "" {
		return toolError("[ERROR] 缺少论文链接 href"), nil
	}

	result, err := DownloadPaper(args.Href, args.SaveDir)
	if err != nil {
		payload := map[string]any{"error": err.Error()}
		if result.Title != "" || result.Abstract != "" || len(result.Keywords) > 0 {
			payload["title"] = result.Title
			payload["authors"] = result.Authors
			payload["keywords"] = result.Keywords
			payload["abstract"] = result.Abstract
			payload["year"] = result.Year
		}
		raw, marshalErr := json.Marshal(payload)
		if marshalErr != nil {
			return toolError(fmt.Sprintf("[ERROR] 下载失败: %v", err)), nil
		}
		return toolError(string(raw)), nil
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return toolError(fmt.Sprintf("[ERROR] JSON序列化失败: %v", err)), nil
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{mcp.NewTextContent(string(raw))},
	}, nil
}

func toolError(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{mcp.NewTextContent(msg)},
		IsError: true,
	}
}

// DownloadPaper 打开摘要页, 顺着 PDF 按钮的跳转把文件存到本机.
func DownloadPaper(href, saveDir string) (downloadResult, error) {
	info, err := GetPaperInfo(href)
	if err != nil {
		return downloadResult{}, err
	}
	result := resultFromInfo(info)
	if strings.TrimSpace(info.PDF) == "" {
		return result, fmt.Errorf("该文献页面没有 PDF 下载按钮. 机构未订购时知网不会给出入口")
	}

	path, n, err := fetchPDF(info.PDF, href, saveDir, info.Title)
	if err == nil {
		result.File = path
		result.Bytes = n
		result.Format = "PDF"
		return result, nil
	}
	if errors.Is(err, ErrNotSubscribed) || errors.Is(err, ErrBlocked) {
		return result, err
	}

	// 订单链接和会话绑在一起, 失败时重新打开摘要页再下一次.
	info, againErr := getPaperInfo(href)
	if againErr == nil && info.PDF != "" {
		result = resultFromInfo(info)
		if path, n, err2 := fetchPDF(info.PDF, href, saveDir, info.Title); err2 == nil {
			result.File = path
			result.Bytes = n
			result.Format = "PDF"
			return result, nil
		} else {
			err = err2
		}
	}
	if errors.Is(err, ErrCookieExpired) {
		if ok, loginErr := RefreshLogin(); ok && loginErr == nil {
			info, err = getPaperInfo(href)
			if err != nil {
				return result, err
			}
			result = resultFromInfo(info)
			if info.PDF == "" {
				return result, fmt.Errorf("重新登录后这篇文献仍然没有 PDF 下载按钮")
			}
			path, n, err = fetchPDF(info.PDF, href, saveDir, info.Title)
			if err == nil {
				result.File = path
				result.Bytes = n
				result.Format = "PDF"
				return result, nil
			}
		}
	}
	return result, err
}

func resultFromInfo(info types.PaperInfo) downloadResult {
	authors := info.Authors
	if authors == nil {
		authors = []string{}
	}
	keywords := info.Keywords
	if keywords == nil {
		keywords = []string{}
	}
	return downloadResult{
		Title:       info.Title,
		Authors:     authors,
		Keywords:    keywords,
		Abstract:    info.Abstract,
		Year:        info.Year,
		OnlineFirst: info.OnlineFirst,
		Institution: LoginInstitution(),
	}
}

func fetchPDF(pdfURL, referer, saveDir, title string) (string, int, error) {
	req, err := http.NewRequest(http.MethodGet, pdfURL, nil)
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Referer", referer)
	resp, err := cnkiDoLimited(req, true, 150<<20)
	if err != nil {
		return "", 0, err
	}
	if resp.Status != http.StatusOK || !isPDF(resp.Body) {
		return "", 0, explainDownloadBody(resp.Status, resp.Body)
	}
	path, err := resolveSavePath(saveDir, title)
	if err != nil {
		return "", 0, err
	}
	if err := os.WriteFile(path, resp.Body, 0o644); err != nil {
		return "", 0, err
	}
	return path, len(resp.Body), nil
}

func isPDF(body []byte) bool {
	return len(body) > 5 && string(body[:5]) == "%PDF-"
}

func explainDownloadBody(status int, body []byte) error {
	sample := body
	if len(sample) > 8000 {
		sample = sample[:8000]
	}
	text := string(sample)
	switch {
	case strings.Contains(text, "未订购") || strings.Contains(text, "没有订购") || strings.Contains(text, "5042") || strings.Contains(text, "5055"):
		return ErrNotSubscribed
	case looksLikeVerify(text):
		return ErrBlocked
	case looksLikeLogin(text):
		return ErrCookieExpired
	case strings.Contains(text, "10012"):
		return fmt.Errorf("知网下载网关返回 10012, 会话没有跟随跳转")
	default:
		return fmt.Errorf("下载结果不是 PDF (HTTP %d, %d 字节)", status, len(body))
	}
}

func resolveSavePath(saveDir, title string) (string, error) {
	target := strings.TrimSpace(saveDir)
	if target == "" {
		if env := strings.TrimSpace(os.Getenv("CNKI_DOWNLOAD_DIR")); env != "" {
			target = env
		} else {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			target = filepath.Join(home, "Downloads", "CNKI")
		}
	}
	if strings.EqualFold(filepath.Ext(target), ".pdf") {
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return "", err
		}
		return absPath(target), nil
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		return "", err
	}
	name := safePDFName(title)
	path := filepath.Join(target, name)
	if _, err := os.Stat(path); err == nil {
		stem := strings.TrimSuffix(name, ".pdf")
		path = filepath.Join(target, stem+"-"+time.Now().Format("150405")+".pdf")
	}
	return absPath(path), nil
}

func absPath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	return abs
}

func safePDFName(title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		title = "cnki-paper"
	}
	replacer := strings.NewReplacer(
		`\`, " ", `/`, " ", `:`, " ", `*`, " ",
		`?`, " ", `"`, " ", `<`, " ", `>`, " ", `|`, " ",
	)
	title = strings.Join(strings.Fields(replacer.Replace(title)), " ")
	title = strings.Trim(title, ". ")
	rs := []rune(title)
	if len(rs) > 80 {
		title = string(rs[:80])
		title = strings.Trim(title, ". ")
	}
	if title == "" {
		title = "cnki-paper"
	}
	return title + ".pdf"
}
