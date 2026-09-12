package main

import (
	"CNKICrawlerMCP/types"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/gocolly/colly/v2"
)

// extractImpactFactors 提取复合/综合影响因子
func extractImpactFactors(evaluateElem *goquery.Selection) (composite, comprehensive float64) {
	// 遍历所有 <p class="hostUnit">
	evaluateElem.Find("p.hostUnit").Each(func(i int, p *goquery.Selection) {
		label := strings.TrimSpace(p.Find("label").Text())
		value := strings.TrimSpace(p.Find("span").Text())

		// 匹配复合影响因子
		if strings.Contains(label, "复合影响因子") {
			// 尝试解析为 float64（处理空值/非法值）
			if f, err := strconv.ParseFloat(value, 64); err == nil {
				composite = f
			}
		}

		// 匹配综合影响因子
		if strings.Contains(label, "综合影响因子") {
			if f, err := strconv.ParseFloat(value, 64); err == nil {
				comprehensive = f
			}
		}
	})
	return composite, comprehensive
}
func extractSourceInfo(sourceElem *goquery.Selection, baseURL *url.URL) types.SourceInfo {
	info := types.SourceInfo{}

	// 情况 1: 有链接的期刊/会议/出版社（优先匹配）
	// 匹配 knavi.cnki.net（期刊导航）或 thinker.cnki.net（图书）
	link := sourceElem.Find("a[href*='knavi/detail'], a[href*='thinker.cnki.net']").First()
	if link.Length() > 0 {
		href, exists := link.Attr("href")
		if exists && href != "" {
			// 转绝对路径（处理相对链接）
			absURL, err := url.Parse(href)
			if err == nil && baseURL != nil {
				info.Link = baseURL.ResolveReference(absURL).String()
			} else {
				info.Link = href
			}
			info.Name = strings.TrimSpace(link.Text())
			return info
		}
	}

	// 情况 2: 无链接的外文期刊/会议（只有文本）
	span := sourceElem.Find("span").First()
	if span.Length() > 0 {
		text := strings.TrimSpace(span.Text())
		if text != "" && text != "—" {
			info.Name = text
			// Link 保持为空
			return info
		}
	}

	// 情况 3: 兜底：直接取 td.source 的文本
	text := strings.TrimSpace(sourceElem.Text())
	text = strings.Join(strings.Fields(text), " ") // 清理多余空格
	if text != "" && text != "—" && text != "..." {
		info.Name = text
	}

	return info
}

type Response struct {
	Success bool    `json:"success"`
	Content Content `json:"content"`
}

type Content struct {
	Title        string        `json:"title"`
	CatalogInfos []CatalogInfo `json:"catalogInfos"`
}

type CatalogInfo struct {
	CataTitle string `json:"cataTitle"`
	Content   string `json:"content"`
	OrderNum  int    `json:"orderNum"`
}

func RestoreArticleText(jsonStr string) (string, error) {
	var resp Response
	if err := json.Unmarshal([]byte(jsonStr), &resp); err != nil {
		return "", fmt.Errorf("JSON 解析失败：%v\n原json信息: %s", err, jsonStr)
	}
	if !resp.Success {
		return "", fmt.Errorf("API 返回失败\n原json信息: %s", jsonStr)
	}
	sort.Slice(resp.Content.CatalogInfos, func(i, j int) bool {
		return resp.Content.CatalogInfos[i].OrderNum < resp.Content.CatalogInfos[j].OrderNum
	})
	var sb strings.Builder
	if resp.Content.Title != "" {
		sb.WriteString(resp.Content.Title)
		sb.WriteString("\n\n")
	}
	for _, chapter := range resp.Content.CatalogInfos {
		sb.WriteString(chapter.CataTitle)
		sb.WriteString("\n")
		sb.WriteString(stripHTML(chapter.Content))
		sb.WriteString("\n\n")
	}

	return sb.String(), nil
}

func stripHTML(htmlStr string) string {
	text := html.UnescapeString(htmlStr)
	reg := regexp.MustCompile(`<[^>]+>`)
	text = reg.ReplaceAllString(text, "")
	text = strings.TrimSpace(text)
	text = regexp.MustCompile(`\s+`).ReplaceAllString(text, " ")
	return text
}
func GetPaperMainBodyParams(href string, cookie string) (types.PaperMainBodyParams, error) {
	redirectUrl := getRedirect(href, cookie)
	params, err := url.Parse(getRedirect(redirectUrl, cookie))
	if err != nil {
		return types.PaperMainBodyParams{}, err
	}
	queryParams := params.Query()
	paperMainBodyParams := types.PaperMainBodyParams{
		Invoice:   url.QueryEscape(queryParams.Get("invoice")),
		FileName:  queryParams.Get("fileName"),
		TableName: queryParams.Get("tableName"),
		DbCode:    queryParams.Get("dbCode"),
	}
	return paperMainBodyParams, nil
}
func getRedirect(href string, cookie string) string {
	var redirectUrl string
	c := colly.NewCollector()
	//c.SetProxy("http://127.0.0.1:8080") // 抓包测试代理
	c.OnRequest(func(r *colly.Request) {
		r.Headers.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
		r.Headers.Set("Cookie", cookie)
		r.Headers.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/114.0.0.0 Safari/537.36")
		r.Headers.Set("Referer", "https://kns.cnki.net/")
	})
	c.SetRedirectHandler(func(r *http.Request, via []*http.Request) error {
		redirectUrl = r.URL.String()
		return http.ErrUseLastResponse
	})
	c.Visit(href)
	return redirectUrl
}
func CheckLogin() (bool, error) {
	if GetGlobalCookie() != "" {
		return true, nil
	}
	return RefreshLogin()
}

// RefreshLogin 强制重新做一次 IP 登录并覆盖全局 Cookie。
// 原实现只在 Cookie 为空时登录，Cookie 过期后不会刷新，导致后续检索静默返回空结果。
func RefreshLogin() (bool, error) {
	cookie, err := IpLogin()
	if err != nil {
		return false, err
	}
	if cookie == "" {
		return false, fmt.Errorf("IP 登录未获取到 Cookie，请确认处于校园网环境，或手动调用 SetGlobalCookie 设置")
	}
	SetGlobalCookie(cookie)
	return true, nil
}
func ParseArgs[T any](args any) (*T, error) {
	argsMap, ok := args.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("参数类型错误: 期望 map[string]interface{}, 得到 %T", args)
	}
	data, err := json.Marshal(argsMap)
	if err != nil {
		return nil, fmt.Errorf("序列化参数失败: %w", err)
	}

	var result T
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("解析参数失败: %w", err)
	}
	return &result, nil
}

// IpLogin 通过访问登录接口获取 Cookie，并返回组合后的 Cookie 字符串
func IpLogin() (string, error) {
	c := colly.NewCollector()

	var Cookie string                     // 最终组合Cookie字符串
	var Cookies = make(map[string]string) // 存储单个cookie的name和value

	c.OnRequest(func(r *colly.Request) {
		r.Headers.Set("Content-Type", "application/json")
	})

	// colly 默认是同步的，PostRaw 返回时回调已执行完毕，无需 WaitGroup。
	// 原实现在 OnResponse / OnError 里各调一次 wg.Done()，请求出错时还会再调一次，
	// 会 panic: sync: negative WaitGroup counter 并打挂整个 MCP 进程。
	c.OnResponse(func(r *colly.Response) {
		var resp types.LoginResponse
		body := strings.Trim(string(r.Body), "()")
		if err := json.Unmarshal([]byte(body), &resp); err != nil {
			return
		}
		if !resp.IsSuccess {
			return
		}
		for _, cookieStr := range r.Headers.Values("Set-Cookie") {
			name, value := parseCookie(cookieStr)
			if name != "" {
				Cookies[name] = value
			}
		}
		Cookie = buildCookieHeader(Cookies)
	})

	c.OnError(func(r *colly.Response, err error) {
		Cookie = ""
	})

	if err := c.PostRaw("https://login.cnki.net/TopLoginCore/api/loginapi/IpLoginFlushPo", []byte("{}")); err != nil {
		return "", err
	}
	return Cookie, nil
}

// buildCookieHeader 将 cookies map 转换为 "name=value; name2=value2" 格式的字符串
func buildCookieHeader(cookies map[string]string) string {
	names := make([]string, 0, len(cookies))
	for name := range cookies {
		names = append(names, name)
	}
	sort.Strings(names) // map 遍历顺序随机，排序保证 Cookie 头稳定
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, name+"="+cookies[name])
	}
	return strings.Join(parts, "; ")
}

// parseCookie 解析单个 Set-Cookie 字符串，返回 name 和 value
func parseCookie(cookieStr string) (string, string) {
	parts := strings.SplitN(cookieStr, ";", 2)
	if len(parts) == 0 {
		return "", ""
	}
	kv := strings.SplitN(strings.TrimSpace(parts[0]), "=", 2)
	if len(kv) != 2 {
		return "", ""
	}
	return strings.TrimSpace(kv[0]), kv[1]
}
