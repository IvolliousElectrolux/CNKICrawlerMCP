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
	Institution     string   `json:"institution,omitempty"`
	Journal         string   `json:"journal,omitempty"`
	DOI             string   `json:"doi,omitempty"`
	File            string   `json:"file,omitempty"`
	Bytes           int      `json:"bytes,omitempty"`
	Format          string   `json:"format,omitempty"`
	Dir             string   `json:"dir,omitempty"`
	Metadata        string   `json:"metadata,omitempty"`
	HTML            string   `json:"html,omitempty"`
	Images          int      `json:"images"`
	TextChars       int      `json:"textChars"`
	MetadataError   string   `json:"metadataError,omitempty"`
	HTMLError       string   `json:"htmlError,omitempty"`
	Import          string   `json:"import,omitempty"`
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

	body, err := fetchPDF(info.PDF, href)
	if err == nil {
		return finishDownload(body, saveDir, info)
	}
	if errors.Is(err, ErrNotSubscribed) || errors.Is(err, ErrBlocked) {
		return result, err
	}

	// 订单链接和会话绑在一起, 失败时重新打开摘要页再下一次.
	info, againErr := getPaperInfo(href)
	if againErr == nil && info.PDF != "" {
		result = resultFromInfo(info)
		if body, err2 := fetchPDF(info.PDF, href); err2 == nil {
			return finishDownload(body, saveDir, info)
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
			body, err = fetchPDF(info.PDF, href)
			if err == nil {
				return finishDownload(body, saveDir, info)
			}
		}
	}
	return result, err
}

func finishDownload(body []byte, saveDir string, info types.PaperInfo) (downloadResult, error) {
	result := resultFromInfo(info)
	dir, pdfPath, err := locatePaper(saveDir, info)
	if err != nil {
		return result, err
	}
	if err := os.WriteFile(pdfPath, body, 0o644); err != nil {
		return result, err
	}
	result.File = pdfPath
	result.Bytes = len(body)
	result.Format = "PDF"
	result.Dir = dir

	exported, htmlErr := exportPDFHTML(body, dir, info.Title)
	htmlPath := ""
	if htmlErr != nil {
		result.HTMLError = htmlErr.Error()
	} else {
		htmlPath = exported.Path
		result.HTML = exported.Path
		result.Images = exported.Images
		result.TextChars = exported.TextChars
	}
	bibPath, bibErr := writeZoteroBib(dir, info, pdfPath, htmlPath)
	if bibErr != nil {
		result.MetadataError = bibErr.Error()
	} else {
		result.Metadata = bibPath
		if htmlPath != "" {
			result.Import = "在 Zotero 里选择 文件 - 导入, 打开 zotero.bib. PDF 和 HTML 会作为附件挂上."
		} else {
			result.Import = "在 Zotero 里选择 文件 - 导入, 打开 zotero.bib. PDF 会作为附件挂上."
		}
	}
	return result, nil
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
		Journal:     info.Journal,
		DOI:         info.DOI,
	}
}

func fetchPDF(pdfURL, referer string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, pdfURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Referer", referer)
	resp, err := cnkiDoLimited(req, true, 150<<20)
	if err != nil {
		return nil, err
	}
	if resp.Status != http.StatusOK || !isPDF(resp.Body) {
		return nil, explainDownloadBody(resp.Status, resp.Body)
	}
	return resp.Body, nil
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
