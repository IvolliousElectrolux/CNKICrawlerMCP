package main

import (
	"CNKICrawlerMCP/types"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/PuerkitoBio/goquery"
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
func GetPaperMainBodyParams(href string) (types.PaperMainBodyParams, error) {
	redirectUrl := getRedirect(href)
	if redirectUrl == "" {
		return types.PaperMainBodyParams{}, fmt.Errorf("在线阅读页没有跳转")
	}
	next := getRedirect(redirectUrl)
	if next == "" {
		return types.PaperMainBodyParams{}, fmt.Errorf("在线阅读页第二次跳转失败")
	}
	params, err := url.Parse(next)
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
func getRedirect(href string) string {
	req, err := http.NewRequest(http.MethodGet, href, nil)
	if err != nil {
		return ""
	}
	resp, err := cnkiDo(req, false)
	if err != nil || resp == nil {
		return ""
	}
	loc := resp.Header.Get("Location")
	if loc == "" {
		return ""
	}
	base, err := url.Parse(href)
	if err != nil {
		return loc
	}
	ref, err := url.Parse(loc)
	if err != nil {
		return loc
	}
	return base.ResolveReference(ref).String()
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
