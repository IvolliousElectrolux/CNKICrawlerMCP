package main

import (
	"context"
	"CNKICrawlerMCP/types"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func main() {
	s := server.NewMCPServer("CNKICrawlerMcp", "1.0.0")
	s.AddTool(mcp.Tool{
		Name:        "ScanPaper",
		Description: "通过关键词进行论文检索（主题字段）。返回标题、作者、来源期刊、发表年份及影响因子。默认按发表时间倒序，Year/Date 取自知网“发表时间”列；OnlineFirst 为 true 时该文献尚无正式刊期，Year 是网络首发日期而非刊期年份",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"value":    map[string]string{"type": "string", "description": "检索关键词（主题字段）"},
				"pageSize": map[string]string{"type": "integer", "description": "期望返回的论文数量，1-50，默认20。知网接口仅支持 10/20/50 三档，传入其他值会被自动向上取整到合法档位"},
				"pageNum":  map[string]string{"type": "integer", "description": "起始页码，从1开始，默认1"},
				"withFactors": map[string]string{"type": "boolean", "description": "是否补全期刊影响因子，默认true。补全需按期刊逐个抓取，50条结果约需1分钟；只要题录信息时设为false可秒回"},
			},
			Required: []string{"value"},
		},
	}, handleScanPaper)
	s.AddTool(mcp.Tool{
		Name:        "GetPaperInfo",
		Description: "通过ScanPaper工具获取的论文链接，获取论文的摘要、发表年份、下载链接、在线阅读链接等信息",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"href": map[string]string{"type": "string", "description": "论文链接"},
			},
			Required: []string{"href"},
		},
	}, handleGetPaperInfo)
	s.AddTool(mcp.Tool{
		Name:        "GetPaperMainBody",
		Description: "通过GetPaperInfo获取到的Html在线阅读链接，获取论文的正文内容",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"href": map[string]string{"type": "string", "description": "Html在线阅读链接"},
			},
			Required: []string{"href"},
		},
	}, handleGetPaperMainBody)
	s.AddTool(mcp.Tool{
		Name:        "SetGlobalCookie",
		Description: "设置CNKIrawlerMCP的全局Cookie，如果用户没有明确要求设置cookie不要使用该工具，系统默认会自动通过IP登录获取Cookie并设置到全局变量中",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"cookie": map[string]string{"type": "string", "description": "Cookie值"},
			},
			Required: []string{"cookie"},
		},
	}, handleSetGlobalCookie)
	s.AddTool(mcp.Tool{
		Name:        "GetGlobalCookie",
		Description: "获取CNKIrawlerMCP的全局Cookie",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
		},
	}, handleGetGlobalCookie)

	server.ServeStdio(s)

	/*
		// Debug 调试
		Cookie, _ := IpLogin()
		if Cookie == "" {
			fmt.Println("[Error] 登录失败，未获取到Cookie")
			return
		}
		//fmt.Println("[Success] 登录获取的Cookie:", Cookie)
		Papers, _ := Search("格罗皮乌斯", 1, 20, Cookie)
		for i := range Papers {
			if Papers[i].SourceLink != "" {
				composite, comprehensive, err := GetSourceFactors(Papers[i].SourceLink, Cookie)
				if err == nil {
					Papers[i].SouceCompositeFactor = composite
					Papers[i].SourceComprehensiveFactor = comprehensive
				}
				time.Sleep(100 * time.Millisecond)
			}
		}
		for _, paper := range Papers {
			fmt.Printf("标题: %s\n作者: %s\n来源: %s\n来源链接: %s\n跳转链接: %s\n 复合影响因子: %.2f\n 综合影响因子: %.2f\n",
				paper.Title, paper.Authors, paper.Source, paper.SourceLink, paper.Href, paper.SouceCompositeFactor, paper.SourceComprehensiveFactor)
		}
	*/
}

func handleGetGlobalCookie(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			mcp.NewTextContent(fmt.Sprintf("[Success] 获取全局Cookie成功: %v", GetGlobalCookie())),
		},
	}, nil
}

type setGlobalCookieArgs struct {
	Cookie string `json:"cookie"`
}

func handleSetGlobalCookie(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args, err := ParseArgs[setGlobalCookieArgs](request.Params.Arguments)
	if err != nil {
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				mcp.NewTextContent(fmt.Sprintf("[ERROR] 参数错误: %v", err)),
			},
			IsError: true,
		}, nil
	}
	SetGlobalCookie(args.Cookie)
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			mcp.NewTextContent(fmt.Sprintf("[Success] 设置全局Cookie成功: %v", GetGlobalCookie())),
		},
	}, nil
}

type scanPaperArgs struct {
	Value    string `json:"value"`
	PageSize int    `json:"pageSize"`
	PageNum  int    `json:"pageNum"`
	// 用指针以区分"未传"和"显式传 false"，未传时默认补全影响因子
	WithFactors *bool `json:"withFactors"`
}

// searchWithRelogin 检索；若发现 Cookie 失效则自动重新 IP 登录后重试一次
func searchWithRelogin(value string, pageNum, pageSize int) ([]types.Paper, error) {
	papers, err := Search(value, pageNum, pageSize, GetGlobalCookie())
	if errors.Is(err, ErrCookieExpired) {
		if ok, loginErr := RefreshLogin(); ok && loginErr == nil {
			return Search(value, pageNum, pageSize, GetGlobalCookie())
		}
	}
	return papers, err
}

// enrichSourceFactors 补充期刊影响因子。
// 同一次检索中同一期刊会被大量重复引用，按 SourceLink 去重后请求数可从几十次降到个位数；
// GetSourceFactors 内部另有 24 小时进程级缓存。
func enrichSourceFactors(papers []types.Paper) {
	type factors struct{ composite, comprehensive float64 }
	done := make(map[string]factors, len(papers))

	for i := range papers {
		link := papers[i].SourceLink
		if link == "" {
			continue
		}
		if v, ok := done[link]; ok {
			papers[i].SouceCompositeFactor = v.composite
			papers[i].SourceComprehensiveFactor = v.comprehensive
			continue
		}
		composite, comprehensive, err := GetSourceFactors(link, GetGlobalCookie())
		if err != nil {
			continue
		}
		done[link] = factors{composite, comprehensive}
		papers[i].SouceCompositeFactor = composite
		papers[i].SourceComprehensiveFactor = comprehensive
	}
}

func handleScanPaper(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if check, err := CheckLogin(); !check || err != nil {
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				mcp.NewTextContent(fmt.Sprintf("[ERROR] 登录失败: %v", err)),
			},
			IsError: true,
		}, nil
	}

	args, err := ParseArgs[scanPaperArgs](request.Params.Arguments)
	if err != nil {
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				mcp.NewTextContent(fmt.Sprintf("[ERROR] 参数错误: %v", err)),
			},
			IsError: true,
		}, nil
	}
	if args.PageSize <= 0 {
		args.PageSize = 20
	}
	if args.PageSize > maxTotalResults {
		args.PageSize = maxTotalResults
	}

	papers, err := searchWithRelogin(args.Value, args.PageNum, args.PageSize)
	if err != nil {
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				mcp.NewTextContent(fmt.Sprintf("[ERROR] 检索失败: %v", err)),
			},
			IsError: true,
		}, nil
	}

	withFactors := args.WithFactors == nil || *args.WithFactors
	if withFactors {
		enrichSourceFactors(papers)
	}

	jsonData, err := json.Marshal(papers)
	if err != nil {
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				mcp.NewTextContent(fmt.Sprintf("[ERROR] JSON序列化失败: %v", err)),
			},
			IsError: true,
		}, nil
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			mcp.NewTextContent(string(jsonData)),
		},
	}, nil
}

type getPaperInfoArgs struct {
	Href string `json:"href"`
}

func handleGetPaperInfo(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if check, err := CheckLogin(); !check || err != nil {
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				mcp.NewTextContent(fmt.Sprintf("[ERROR] 登录失败: %v", err)),
			},
			IsError: true,
		}, nil
	}

	args, err := ParseArgs[getPaperInfoArgs](request.Params.Arguments)
	if err != nil {
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				mcp.NewTextContent(fmt.Sprintf("[ERROR] 参数错误: %v", err)),
			},
			IsError: true,
		}, nil
	}
	paperInfo, err := GetPaperInfo(args.Href, GetGlobalCookie())
	if err != nil {
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				mcp.NewTextContent(fmt.Sprintf("[ERROR] 获取论文信息失败: %v", err)),
			},
			IsError: true,
		}, nil
	}
	jsonData, err := json.Marshal(paperInfo)
	if err != nil {
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				mcp.NewTextContent(fmt.Sprintf("[ERROR] JSON序列化失败: %v", err)),
			},
			IsError: true,
		}, nil
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			mcp.NewTextContent(string(jsonData)),
		},
	}, nil
}

type getPaperMainBodyArgs struct {
	Href string `json:"href"`
}

func handleGetPaperMainBody(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if check, err := CheckLogin(); !check || err != nil {
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				mcp.NewTextContent(fmt.Sprintf("[ERROR] 登录失败: %v", err)),
			},
			IsError: true,
		}, nil
	}

	args, err := ParseArgs[getPaperMainBodyArgs](request.Params.Arguments)
	if err != nil {
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				mcp.NewTextContent(fmt.Sprintf("[ERROR] 参数错误: %v", err)),
			},
			IsError: true,
		}, nil
	}
	paperMainBody, err := GetPaperMainBody(args.Href, GetGlobalCookie())
	if err != nil {
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				mcp.NewTextContent(fmt.Sprintf("[ERROR] 获取论文正文失败: %v", err)),
			},
			IsError: true,
		}, nil
	}
	jsonData, err := json.Marshal(paperMainBody)
	if err != nil {
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				mcp.NewTextContent(fmt.Sprintf("[ERROR] JSON序列化失败: %v", err)),
			},
			IsError: true,
		}, nil
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			mcp.NewTextContent(string(jsonData)),
		},
	}, nil
}
