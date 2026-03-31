package main

import (
	"CNKICrawlerMCP/types"
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"strings"
	"sync"

	"github.com/PuerkitoBio/goquery"
	"github.com/gocolly/colly/v2"
)

func Search(value string, pageNum int, pageSize int, cookie string) ([]types.Paper, error) {
	item := types.Item{
		Field:    "SU",
		Value:    value,
		Operator: "TOPRANK",
		Logic:    0,
		Title:    "主题",
	}
	qGroup := types.QGroup{
		Key:        "Subject",
		Title:      "",
		Logic:      0,
		Items:      []types.Item{item},
		ChildItems: []types.Item{},
	}
	qNode := types.QNode{
		QGroup: []types.QGroup{qGroup},
	}
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

	scanPostParam := types.ScanPostParam{
		QueryJson:  string(jsonData),
		PageNum:    pageNum,
		PageSize:   pageSize,
		ProductStr: "YSTT4HG0,LSTPFY1C,RMJLXHZ3,JQIRZIYA,JUP3MUPD,1UR4K4HZ,BPBAFJ5S,R79MZMCB,MPMFIG1A,WQ0UVIAA,NB3BWEHK,XVLO76FD,HR1YT1Z9,BLZOG7CK,PWFIRAGL,EMRPGLPA,J708GVCE,ML4DRIDX,NLBO1Z6R,NN3FJMUV,",
		SearchFrom: "资源范围：总库",                  // 总库
		Turnpage:   "vLP2bNpghntZLRq9Q5Y7Qg!!", // 第9页的turnpage
	}

	postData := map[string]string{
		"QueryJson":  scanPostParam.QueryJson,
		"pageNum":    fmt.Sprintf("%d", scanPostParam.PageNum),
		"pageSize":   fmt.Sprintf("%d", scanPostParam.PageSize),
		"productStr": scanPostParam.ProductStr,
		"searchFrom": scanPostParam.SearchFrom,
		"turnpage":   scanPostParam.Turnpage,
	}

	c := colly.NewCollector()
	var wg sync.WaitGroup
	wg.Add(1)

	c.OnRequest(func(r *colly.Request) {
		r.Headers.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
		r.Headers.Set("Cookie", cookie)
		r.Headers.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/114.0.0.0 Safari/537.36")
		r.Headers.Set("Referer", "https://kns.cnki.net/")
	})
	c.OnResponse(func(r *colly.Response) {
		defer wg.Done()
	})
	c.OnError(func(r *colly.Response, err error) {
		defer wg.Done()
	})
	var papers []types.Paper
	c.OnHTML("table.result-table-list tbody tr", func(e *colly.HTMLElement) {
		titleElem := e.DOM.Find("td.name a.fz14")
		if titleElem.Length() == 0 {
			return
		}
		href, _ := titleElem.Attr("href")
		title := strings.TrimSpace(titleElem.Text())
		if href == "" || strings.HasPrefix(href, "javascript:") {
			return
		}

		// 可选：只收集 abstract 类型的论文链接
		if !strings.Contains(href, "/kcms2/article/abstract") {
			return
		}

		var authors []string
		e.DOM.Find("td.author a.KnowledgeNetLink").Each(func(i int, authorElem *goquery.Selection) {
			author := strings.TrimSpace(authorElem.Text())
			if author != "" {
				authors = append(authors, author)
			}
		})

		baseURL, _ := url.Parse(e.Request.URL.String())
		sourceInfo := extractSourceInfo(e.DOM.Find("td.source"), baseURL)
		papers = append(papers, types.Paper{
			Title:      title,
			Authors:    authors,
			Source:     sourceInfo.Name,
			SourceLink: sourceInfo.Link,
			Href:       href,
		})
	})
	if err := c.Post("https://kns.cnki.net/kns8s/brief/grid", postData); err != nil {
		wg.Done()
		return nil, err
	}
	wg.Wait()
	// fmt.Println(papers)
	return papers, nil
}
func GetPaperInfo(href string, cookie string) (types.PaperInfo, error) {
	var paperInfo types.PaperInfo
	c := colly.NewCollector()
	var wg sync.WaitGroup
	wg.Add(1)
	c.OnRequest(func(r *colly.Request) {
		r.Headers.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
		r.Headers.Set("Cookie", cookie)
		r.Headers.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/114.0.0.0 Safari/537.36")
		r.Headers.Set("Referer", "https://kns.cnki.net/")
	})
	c.OnResponse(func(r *colly.Response) {
		defer wg.Done()
		// fmt.Println(string(r.Body))
	})
	c.OnError(func(r *colly.Response, err error) {
		defer wg.Done()
		fmt.Println("[ERROR] Request failed:", err)
	})

	c.OnHTML("#cajDown[href]", func(e *colly.HTMLElement) {
		href := e.Attr("href")
		if href != "" {
			paperInfo.CAJ = e.Request.AbsoluteURL(href)
			// fmt.Println("[DEBUG] CAJ 下载链接:", links.CAJ)
		}
	})
	c.OnHTML("#pdfDown[href]", func(e *colly.HTMLElement) {
		href := e.Attr("href")
		if href != "" {
			paperInfo.PDF = e.Request.AbsoluteURL(href)
			// fmt.Println("PDF 下载链接:", links.PDF)
		}
	})
	c.OnHTML(".btn-html a[href]", func(e *colly.HTMLElement) {
		href := e.Attr("href")
		if href != "" {
			paperInfo.HTML = e.Request.AbsoluteURL(href)
			// fmt.Println("HTML 链接:", links.HTML)
		}
	})
	c.OnHTML(`input#abstract_text[type="hidden"]`, func(e *colly.HTMLElement) {
		abstract := e.Attr("value")
		if abstract != "" {
			paperInfo.Abstract = html.UnescapeString(abstract)
		}
	})
	if err := c.Visit(href); err != nil {
		wg.Done()
		return paperInfo, err
	}
	wg.Wait()
	return paperInfo, nil
}
func GetPaperMainBody(href string, cookie string) (string, error) {
	params, err := GetPaperMainBodyParams(href, cookie)
	if err != nil {
		return "", err
	}
	var paperMainBodyJson string
	c := colly.NewCollector()
	var wg sync.WaitGroup
	wg.Add(1)
	c.OnRequest(func(r *colly.Request) {
		r.Headers.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
		r.Headers.Set("Cookie", cookie)
		r.Headers.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/114.0.0.0 Safari/537.36")
		r.Headers.Set("Referer", "https://kns.cnki.net/")
	})
	c.OnResponse(func(r *colly.Response) {
		defer wg.Done()
		paperMainBodyJson = string(r.Body)
	})
	c.OnError(func(r *colly.Response, err error) {
		defer wg.Done()
		fmt.Println("[ERROR] Request failed:", err)
	})
	c.Visit("https://kns.cnki.net/nzkhtml/knsread/litNotes/getPaperInfo?fileName=" + params.FileName + "&tableName=" + params.TableName + "&dbCode=" + params.DbCode + "&invoice=" + params.Invoice)
	wg.Wait()
	paperMainBody, err := RestoreArticleText(paperMainBodyJson)
	if err != nil {
		return "", err
	}
	return paperMainBody, nil
}
func GetSourceFactors(href string, cookie string) (float64, float64, error) {
	var SouceCompositeFactor float64
	var SourceComprehensiveFactor float64
	c := colly.NewCollector()
	var wg sync.WaitGroup
	wg.Add(1)

	c.OnRequest(func(r *colly.Request) {
		r.Headers.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
		r.Headers.Set("Cookie", cookie)
		r.Headers.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/114.0.0.0 Safari/537.36")
		r.Headers.Set("Referer", "https://kns.cnki.net/")
	})
	c.OnResponse(func(r *colly.Response) {
		defer wg.Done()
	})
	c.OnError(func(r *colly.Response, err error) {
		defer wg.Done()
	})
	c.OnHTML("ul#evaluateInfo", func(e *colly.HTMLElement) {
		composite, comprehensive := extractImpactFactors(e.DOM)
		SouceCompositeFactor = composite
		SourceComprehensiveFactor = comprehensive
	})
	if err := c.Visit(href); err != nil {
		wg.Done()
		return 0, 0, err
	}
	wg.Wait()
	return SouceCompositeFactor, SourceComprehensiveFactor, nil
}
