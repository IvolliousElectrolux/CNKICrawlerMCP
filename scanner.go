package main

import (
	"CNKICrawlerMCP/types"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/PuerkitoBio/goquery"
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
func fetchSearchPage(value string, pageNum, pageSize int) (*searchPage, error) {
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

	form := url.Values{}
	for k, v := range postData {
		form.Set(k, v)
	}
	resp, err := cnkiPostForm(searchEndpoint, form, "https://kns.cnki.net/kns8s/defaultresult/index")
	if err != nil {
		return nil, fmt.Errorf("检索请求失败: %w", err)
	}
	body := string(resp.Body)

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
		if paper, ok := parsePaperRow(row, baseURL); ok {
			page.Papers = append(page.Papers, paper)
		}
	})

	return page, nil
}

// onlineFirstMark 结果表里标记"尚无正式刊期"的角标文字
const onlineFirstMark = "网络首发"

// 知网结果表 `td.date`（表头"发表时间"）的取值形态不止一种：
//   - 期刊/学位/会议/报纸： "2023-05-15"
//   - 网络首发：           "2026-10-16 10:52"（带时分）
//   - 少量旧数据/带版次：   "2023年05期"、"2023-05-15(A1)"
//
// 统一取其中出现的第一个 19xx/20xx 四位年份；取不到时返回 0 而不是瞎猜。
var yearRe = regexp.MustCompile(`(?:19|20)\d{2}`)

// pubRe 匹配已正式出版的题录, 如 "2026 ,35 (19) : 86-89".
var pubRe = regexp.MustCompile(`(?:19|20)\d{2}\s*[,，]\s*(\d+)\s*[（(]\s*([^)）]+)\s*[)）]\s*[:：]\s*([0-9A-Za-z]+(?:\s*[-–—~～]\s*[0-9A-Za-z]+)?)`)

var doiRe = regexp.MustCompile(`10\.\d{4,9}/[-._;()/:A-Za-z0-9]+`)

// extractPublishDate 解析 `td.date` 单元格，返回发表时间原文与年份。
func extractPublishDate(dateElem *goquery.Selection) (date string, year int) {
	date = strings.Join(strings.Fields(strings.TrimSpace(dateElem.Text())), " ")
	if date == "" || date == "—" {
		return "", 0
	}
	if s := yearRe.FindString(date); s != "" {
		year, _ = strconv.Atoi(s)
	}
	return date, year
}

// parsePaperRow 把结果表的一行转换成 Paper；
// ok=false 表示这一行不是可用的论文记录（缺标题，或标题链接不是摘要页）。
func parsePaperRow(row *goquery.Selection, baseURL *url.URL) (types.Paper, bool) {
	titleElem := row.Find("td.name a.fz14").First()
	if titleElem.Length() == 0 {
		return types.Paper{}, false
	}
	href, _ := titleElem.Attr("href")
	href = strings.TrimSpace(href)
	if href == "" || strings.HasPrefix(href, "javascript:") {
		return types.Paper{}, false
	}
	if !strings.Contains(href, "/kcms2/article/abstract") {
		return types.Paper{}, false
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
	publishDate, year := extractPublishDate(row.Find("td.date"))

	return types.Paper{
		Title:      strings.TrimSpace(titleElem.Text()),
		Authors:    authors,
		Source:     sourceInfo.Name,
		SourceLink: sourceInfo.Link,
		Year:       year,
		Date:       publishDate,
		// 网络首发的论文尚无正式刊期，td.date 给的是网络首发日期，
		// 年份与最终刊期年份可能不同，单独标出以免被当成刊期年份使用。
		OnlineFirst: strings.Contains(row.Find("b.marktip").Text(), onlineFirstMark),
		Href:        href,
	}, true
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
func Search(value string, pageNum int, pageSize int) ([]types.Paper, error) {
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
		page, err := fetchSearchPage(value, pageNum, perPage)
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

func GetPaperInfo(href string) (types.PaperInfo, error) {
	info, err := getPaperInfo(href)
	if errors.Is(err, ErrCookieExpired) {
		if ok, loginErr := RefreshLogin(); ok && loginErr == nil {
			return getPaperInfo(href)
		}
	}
	return info, err
}

func getPaperInfo(href string) (types.PaperInfo, error) {
	var paperInfo types.PaperInfo
	resp, err := cnkiGet(href, knsHome)
	if err != nil {
		return paperInfo, err
	}
	body := string(resp.Body)
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(body))
	if err != nil {
		return paperInfo, fmt.Errorf("解析详情页失败: %w", err)
	}

	hasPaper := doc.Find("div.wx-tit h1, #abstract_text, #pdfDown, #cajDown").Length() > 0
	if !hasPaper {
		switch {
		case looksLikeVerify(body):
			return paperInfo, ErrBlocked
		case looksLikeLogin(body):
			return paperInfo, ErrCookieExpired
		default:
			return paperInfo, fmt.Errorf("详情页不是论文页 (HTTP %d, 长度 %d)", resp.Status, len(body))
		}
	}

	if h1 := doc.Find("div.wx-tit h1").First(); h1.Length() > 0 {
		clone := h1.Clone()
		clone.Find("i, span, script").Remove()
		paperInfo.Title = strings.Join(strings.Fields(clone.Text()), " ")
	}
	doc.Find("#authorpart a").Each(func(_ int, a *goquery.Selection) {
		if name := strings.TrimSpace(a.Text()); name != "" {
			paperInfo.Authors = append(paperInfo.Authors, name)
		}
	})
	paperInfo.Keywords = parseKeywords(doc.Find("p.keywords").First())
	if v, ok := doc.Find("#abstract_text").Attr("value"); ok && strings.TrimSpace(v) != "" {
		paperInfo.Abstract = html.UnescapeString(strings.TrimSpace(v))
	} else if summary := strings.TrimSpace(doc.Find("#ChDivSummary").Text()); summary != "" {
		paperInfo.Abstract = strings.Join(strings.Fields(summary), " ")
	}
	paperInfo.PDF = absRef(href, attrHref(doc.Find("#pdfDown").First()))
	paperInfo.HasPDF = paperInfo.PDF != ""
	paperInfo.CAJ = absRef(href, attrHref(doc.Find("#cajDown").First()))
	paperInfo.HTML = absRef(href, attrHref(doc.Find(".btn-html a").First()))

	topTip := doc.Find("div.top-tip").First().Text()
	headTime := doc.Find("div.head-time").First().Text()
	paperInfo.Year, paperInfo.OnlineFirst = pickYearFromDetail(topTip, headTime)
	paperInfo.URL = href
	fillPublication(doc, &paperInfo)
	if paperInfo.Authors == nil {
		paperInfo.Authors = []string{}
	}
	if paperInfo.Keywords == nil {
		paperInfo.Keywords = []string{}
	}
	return paperInfo, nil
}

func attrHref(sel *goquery.Selection) string {
	if sel == nil || sel.Length() == 0 {
		return ""
	}
	href, _ := sel.Attr("href")
	href = strings.TrimSpace(href)
	if href == "" || strings.HasPrefix(href, "javascript:") {
		return ""
	}
	return href
}

func absRef(base, href string) string {
	if href == "" {
		return ""
	}
	if strings.HasPrefix(href, "http://") || strings.HasPrefix(href, "https://") {
		return href
	}
	bu, err := url.Parse(base)
	if err != nil {
		return href
	}
	ru, err := url.Parse(href)
	if err != nil {
		return href
	}
	return bu.ResolveReference(ru).String()
}

func parseKeywords(sel *goquery.Selection) []string {
	if sel == nil || sel.Length() == 0 {
		return nil
	}
	var ks []string
	seen := map[string]bool{}
	add := func(raw string) {
		text := strings.Trim(strings.Join(strings.Fields(raw), " "), ";；,， ")
		if text == "" || seen[text] {
			return
		}
		seen[text] = true
		ks = append(ks, text)
	}
	sel.Find("a").Each(func(_ int, a *goquery.Selection) {
		add(a.Text())
	})
	if len(ks) > 0 {
		return ks
	}
	for _, part := range strings.FieldsFunc(sel.Text(), func(r rune) bool {
		return r == ';' || r == '；'
	}) {
		add(part)
	}
	return ks
}

// pickYearFromDetail 从详情页取发表年份。
// 已正式出版的论文，年份在题录行里（topTip，如 "中国商论 . 2026 ,35 (19)"）；
// 网络首发的论文还没有卷期号，题录行里只有刊名，年份只在网络首发时间（headTime）里。
// 所以判断依据是"哪儿真的能取到年份"，而不是"元素存不存在"。
func pickYearFromDetail(topTip, headTime string) (year int, onlineFirst bool) {
	src := topTip
	if yearRe.FindStringIndex(src) == nil {
		src = headTime
	}
	if s := yearRe.FindString(src); s != "" {
		year, _ = strconv.Atoi(s)
	}
	return year, strings.Contains(headTime, onlineFirstMark)
}

// fillPublication 从题录行取出期刊, 卷, 期, 页码和 DOI.
func fillPublication(doc *goquery.Document, info *types.PaperInfo) {
	tip := doc.Find("div.top-tip").First()
	tip.Find("a").Each(func(_ int, a *goquery.Selection) {
		if info.Journal != "" {
			return
		}
		text := strings.Trim(strings.Join(strings.Fields(a.Text()), " "), ".。 ")
		if text == "" || strings.Contains(text, "查看") || strings.Contains(text, "帮助") {
			return
		}
		info.Journal = text
	})
	text := strings.Join(strings.Fields(tip.Text()), " ")
	text = strings.ReplaceAll(text, "查看该刊数据库收录来源", "")
	if info.Journal == "" {
		if loc := yearRe.FindStringIndex(text); loc != nil && loc[0] > 0 {
			name := strings.Trim(text[:loc[0]], ".。,， ")
			if name != "" && !strings.Contains(name, "查看") {
				info.Journal = name
			}
		}
	}
	if m := pubRe.FindStringSubmatch(text); len(m) == 4 {
		info.Volume = m[1]
		info.Issue = strings.TrimSpace(m[2])
		info.Pages = strings.TrimSpace(m[3])
	}
	info.DOI = findDOI(doc)
}

func findDOI(doc *goquery.Document) string {
	found := ""
	doc.Find("div.row, li").Each(func(_ int, row *goquery.Selection) {
		if found != "" {
			return
		}
		label := row.Find(".rowtit").Text()
		if strings.Contains(strings.ToUpper(label), "DOI") {
			found = doiRe.FindString(row.Text())
		}
	})
	if found == "" {
		found = doiRe.FindString(doc.Find("div.brief").Text())
	}
	return strings.TrimRight(found, ".,;)]")
}

func GetPaperMainBody(href string) (string, error) {
	params, err := GetPaperMainBodyParams(href)
	if err != nil {
		return "", err
	}
	rawURL := "https://kns.cnki.net/nzkhtml/knsread/litNotes/getPaperInfo?fileName=" +
		params.FileName + "&tableName=" + params.TableName + "&dbCode=" + params.DbCode +
		"&invoice=" + params.Invoice
	resp, err := cnkiGet(rawURL, href)
	if err != nil {
		return "", err
	}
	return RestoreArticleText(string(resp.Body))
}

// GetSourceFactors 获取期刊影响因子；同一期刊在进程内缓存 24 小时，避免重复请求
func GetSourceFactors(href string) (float64, float64, error) {
	if entry, ok := getCachedFactor(href); ok {
		if entry.Failed {
			return 0, 0, fmt.Errorf("该期刊页此前未解析到评价信息（%.0f 分钟内不再重试）: %s",
				factorFailTTL.Minutes(), href)
		}
		return entry.Composite, entry.Comprehensive, nil
	}

	resp, err := cnkiGet(href, knsHome)
	if err != nil {
		return 0, 0, err
	}
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(resp.Body)))
	if err != nil {
		return 0, 0, err
	}
	eval := doc.Find("ul#evaluateInfo")
	found := eval.Length() > 0
	var compositeFactor, comprehensiveFactor float64
	if found {
		compositeFactor, comprehensiveFactor = extractImpactFactors(eval)
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
