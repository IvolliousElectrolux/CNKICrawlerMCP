package main

import (
	"CNKICrawlerMCP/types"
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"strconv"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/gocolly/colly/v2"
)

const searchEndpoint = "https://kns.cnki.net/kns8s/brief/grid"

// 知网检索接口只接受这几个 pageSize 取值。
// 传入其他值（如 30/40/60/80/100）时服务端不会报错，而是静默返回一个不含结果表格的页面，
// 旧代码因此拿到空列表并把它序列化成 JSON null —— 这正是"检索间歇性返回 null"的根因。
var validPageSizes = []int{10, 20, 50}

// maxTotalResults 单次检索累计返回的论文数上限。
// 实测知网单页最多返回 50 条；更深翻页依赖请求里硬编码的 turnpage 令牌（会话相关），
// 翻页会拿回重复数据，因此这里如实限制为 50，而不是对外宣称 100 却只给得出 50。
const maxTotalResults = 50

// normalizePageSize 把任意期望数量映射到知网接受的合法 pageSize（向上取最接近的一档）
func normalizePageSize(want int) int {
	if want <= 0 {
		return 20
	}
	for _, v := range validPageSizes {
		if want <= v {
			return v
		}
	}
	return validPageSizes[len(validPageSizes)-1]
}

func newCollector(cookie string) *colly.Collector {
	c := colly.NewCollector()
	c.OnRequest(func(r *colly.Request) {
		r.Headers.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
		r.Headers.Set("Cookie", cookie)
		r.Headers.Set("User-Agent", userAgent)
		r.Headers.Set("Referer", "https://kns.cnki.net/")
	})
	return c
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func looksLikeVerify(body string) bool {
	return containsAny(body, "滑动验证", "人机验证", "安全验证", "请完成验证", "拖动滑块", "captcha", "verifycode")
}

func looksLikeLogin(body string) bool {
	return containsAny(body, "用户登录", "登录知网", "账号登录", "个人登录", "IP登录", "login.cnki.net")
}

// searchPage 单页检索结果
type searchPage struct {
	Papers    []types.Paper
	Total     int // 命中总数，-1 表示未解析到
	TotalPage int // 总页数，0 表示未解析到
}

// fetchSearchPage 抓取并解析单页检索结果
func fetchSearchPage(value string, pageNum, pageSize int, cookie string) (*searchPage, error) {
	item := types.Item{Field: "SU", Value: value, Operator: "TOPRANK", Logic: 0, Title: "主题"}
	qGroup := types.QGroup{Key: "Subject", Title: "", Logic: 0, Items: []types.Item{item}, ChildItems: []types.Item{}}
	qNode := types.QNode{QGroup: []types.QGroup{qGroup}}
	queryJson := types.QueryJson{
		Platform:   "",
		Resource:   "CROSSDB",
		Classid:    "WD0FTY92",
		Products:   "CJFQ,CAPJ,WWJD,CJTL,CDFD,CMFD,CPFD,IPFD,CPVD,WWPD,CCND,SCSF,SCHF,SCSD,SOSD,SNAD,CCJD,WBFD,WWBD,CCVD,CJFN",
		QNode:      qNode,
		ExScope:    1,
		SearchType: 2,
		Rlang:      "BOTH",
		KuaKuCode:  "YSTT4HG0,LSTPFY1C,JUP3MUPD,MPMFIG1A,WQ0UVIAA,BLZOG7CK,PWFIRAGL,EMRPGLPA,NLBO1Z6R,NN3FJMUV",
		Expands:    make(map[string]interface{}),
		SearchFrom: 4,
	}

	jsonData, err := json.Marshal(queryJson)
	if err != nil {
		return nil, err
	}

	postData := map[string]string{
		"QueryJson":  string(jsonData),
		"pageNum":    strconv.Itoa(pageNum),
		"pageSize":   strconv.Itoa(pageSize),
		"productStr": "YSTT4HG0,LSTPFY1C,RMJLXHZ3,JQIRZIYA,JUP3MUPD,1UR4K4HZ,BPBAFJ5S,R79MZMCB,MPMFIG1A,WQ0UVIAA,NB3BWEHK,XVLO76FD,HR1YT1Z9,BLZOG7CK,PWFIRAGL,EMRPGLPA,J708GVCE,ML4DRIDX,NLBO1Z6R,NN3FJMUV,",
		"searchFrom": "资源范围：总库",
		"turnpage":   "vLP2bNpghntZLRq9Q5Y7Qg!!",
	}

	var body string
	cnkiThrottle.waitTurn()

	c := newCollector(cookie)
	c.OnResponse(func(r *colly.Response) {
		body = string(r.Body)
	})
	c.OnError(func(r *colly.Response, err error) {
		if r != nil {
			body = string(r.Body)
		}
	})

	if err := c.Post(searchEndpoint, postData); err != nil && strings.TrimSpace(body) == "" {
		return nil, fmt.Errorf("检索请求失败: %w", err)
	}

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("解析检索响应失败: %w", err)
	}

	hasToolbar := doc.Find("#countPageDiv").Length() > 0
	hasTable := doc.Find("table.result-table-list").Length() > 0

	if !hasToolbar && !hasTable {
		switch {
		// 先判登录：登录页里也常含"验证"字样，顺序反了会把 Cookie 失效误报成风控
		case looksLikeLogin(body):
			return nil, ErrCookieExpired
		case looksLikeVerify(body):
			return nil, ErrBlocked
		default:
			return nil, fmt.Errorf("知网返回了非预期的页面（响应长度 %d），可能被限流或接口已变更", len(body))
		}
	}

	page := &searchPage{Papers: make([]types.Paper, 0, pageSize), Total: -1}

	if em := doc.Find("#countPageDiv em").First(); em.Length() > 0 {
		raw := strings.ReplaceAll(strings.TrimSpace(em.Text()), ",", "")
		if n, err := strconv.Atoi(raw); err == nil {
			page.Total = n
		}
	}
	if mark := doc.Find("#countPageDiv span.countPageMark").First(); mark.Length() > 0 {
		page.TotalPage = parseTotalPage(mark.Text(), mark.AttrOr("data-pagenum", ""))
	}

	baseURL, _ := url.Parse(searchEndpoint)
	doc.Find("table.result-table-list tbody tr").Each(func(_ int, row *goquery.Selection) {
		titleElem := row.Find("td.name a.fz14").First()
		if titleElem.Length() == 0 {
			return
		}
		href, _ := titleElem.Attr("href")
		href = strings.TrimSpace(href)
		if href == "" || strings.HasPrefix(href, "javascript:") {
			return
		}
		if !strings.Contains(href, "/kcms2/article/abstract") {
			return
		}
		if !strings.HasPrefix(href, "http") {
			href = "https://kns.cnki.net" + href
		}

		var authors []string
		row.Find("td.author a.KnowledgeNetLink").Each(func(_ int, a *goquery.Selection) {
			if name := strings.TrimSpace(a.Text()); name != "" {
				authors = append(authors, name)
			}
		})

		sourceInfo := extractSourceInfo(row.Find("td.source"), baseURL)
		page.Papers = append(page.Papers, types.Paper{
			Title:      strings.TrimSpace(titleElem.Text()),
			Authors:    authors,
			Source:     sourceInfo.Name,
			SourceLink: sourceInfo.Link,
			Href:       href,
		})
	})

	return page, nil
}

// parseTotalPage 从 "1/300" 或 data-pagenum="300" 解析总页数
func parseTotalPage(text, dataPageNum string) int {
	if n, err := strconv.Atoi(strings.TrimSpace(dataPageNum)); err == nil && n > 0 {
		return n
	}
	if i := strings.LastIndex(text, "/"); i >= 0 {
		if n, err := strconv.Atoi(strings.TrimSpace(text[i+1:])); err == nil && n > 0 {
			return n
		}
	}
	return 0
}

// Search 按关键词检索。pageSize 表示期望累计返回的论文数（内部会自动向知网合法档位取整并按需翻页）。
// 返回值永远是非 nil 切片；真正无结果时返回空切片，被风控或 Cookie 失效时返回对应错误。
func Search(value string, pageNum int, pageSize int, cookie string) ([]types.Paper, error) {
	if strings.TrimSpace(value) == "" {
		return nil, fmt.Errorf("检索关键词不能为空")
	}
	if pageNum < 1 {
		pageNum = 1
	}
	want := pageSize
	if want <= 0 {
		want = 20
	}
	if want > maxTotalResults {
		want = maxTotalResults
	}
	perPage := normalizePageSize(want)

	papers := make([]types.Paper, 0, want)
	seen := make(map[string]bool, want)

	for len(papers) < want {
		page, err := fetchSearchPage(value, pageNum, perPage, cookie)
		if err != nil {
			// 已经拿到部分结果时不要把整次检索判为失败
			if len(papers) > 0 {
				break
			}
			return nil, err
		}
		if len(page.Papers) == 0 {
			break
		}

		added := 0
		for _, p := range page.Papers {
			if seen[p.Href] {
				continue
			}
			seen[p.Href] = true
			papers = append(papers, p)
			added++
			if len(papers) >= want {
				break
			}
		}
		// 整页都是重复，说明已经翻到底，避免死循环
		if added == 0 {
			break
		}

		pageNum++
		if page.TotalPage > 0 && pageNum > page.TotalPage {
			break
		}
	}

	return papers, nil
}

func GetPaperInfo(href string, cookie string) (types.PaperInfo, error) {
	var paperInfo types.PaperInfo
	cnkiThrottle.waitTurn()

	c := newCollector(cookie)
	c.OnHTML("#cajDown[href]", func(e *colly.HTMLElement) {
		paperInfo.CAJ = e.Request.AbsoluteURL(e.Attr("href"))
	})
	c.OnHTML("#pdfDown[href]", func(e *colly.HTMLElement) {
		paperInfo.PDF = e.Request.AbsoluteURL(e.Attr("href"))
	})
	c.OnHTML(".btn-html a[href]", func(e *colly.HTMLElement) {
		paperInfo.HTML = e.Request.AbsoluteURL(e.Attr("href"))
	})
	c.OnHTML(`input#abstract_text[type="hidden"]`, func(e *colly.HTMLElement) {
		if abstract := e.Attr("value"); abstract != "" {
			paperInfo.Abstract = html.UnescapeString(abstract)
		}
	})

	if err := c.Visit(href); err != nil {
		return paperInfo, err
	}
	return paperInfo, nil
}

func GetPaperMainBody(href string, cookie string) (string, error) {
	params, err := GetPaperMainBodyParams(href, cookie)
	if err != nil {
		return "", err
	}

	var paperMainBodyJson string
	cnkiThrottle.waitTurn()

	c := newCollector(cookie)
	c.OnResponse(func(r *colly.Response) {
		paperMainBodyJson = string(r.Body)
	})
	c.OnError(func(r *colly.Response, err error) {
		fmt.Println("[ERROR] Request failed:", err)
	})

	if err := c.Visit("https://kns.cnki.net/nzkhtml/knsread/litNotes/getPaperInfo?fileName=" +
		params.FileName + "&tableName=" + params.TableName + "&dbCode=" + params.DbCode +
		"&invoice=" + params.Invoice); err != nil {
		return "", err
	}

	return RestoreArticleText(paperMainBodyJson)
}

// GetSourceFactors 获取期刊影响因子；同一期刊在进程内缓存 24 小时，避免重复请求
func GetSourceFactors(href string, cookie string) (float64, float64, error) {
	if entry, ok := getCachedFactor(href); ok {
		if entry.Failed {
			return 0, 0, fmt.Errorf("该期刊页此前未解析到评价信息（%.0f 分钟内不再重试）: %s",
				factorFailTTL.Minutes(), href)
		}
		return entry.Composite, entry.Comprehensive, nil
	}

	var compositeFactor float64
	var comprehensiveFactor float64
	var found bool
	cnkiThrottle.waitTurn()

	c := newCollector(cookie)
	c.OnHTML("ul#evaluateInfo", func(e *colly.HTMLElement) {
		compositeFactor, comprehensiveFactor = extractImpactFactors(e.DOM)
		found = true
	})

	if err := c.Visit(href); err != nil {
		return 0, 0, err
	}

	// 只有确实解析到评价信息才写长缓存；失败写入短 TTL 负缓存，
	// 避免每次检索都为同一批期刊重复发请求。
	if !found {
		putFailedFactor(href)
		return 0, 0, fmt.Errorf("未能从期刊页解析到评价信息: %s", href)
	}

	putCachedFactor(href, compositeFactor, comprehensiveFactor)
	return compositeFactor, comprehensiveFactor, nil
}
