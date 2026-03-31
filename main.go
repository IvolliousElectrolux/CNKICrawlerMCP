package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func main() {
	s := server.NewMCPServer("CNKICrawlerMcp", "1.0.0")
	s.AddTool(mcp.Tool{
		Name:        "ScanPaper",
		Description: "通过关键词进行论文检索",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"value":    map[string]string{"type": "string", "description": "关键词"},
				"pageSize": map[string]string{"type": "integer", "description": "结果数量，最好小于80，最大不超过100"},
			},
			Required: []string{"value"},
		},
	}, handleScanPaper)
	s.AddTool(mcp.Tool{
		Name:        "GetPaperInfo",
		Description: "通过ScanPaper工具获取的论文链接，获取论文的摘要、下载链接、在线阅读链接等信息",
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

type scanPaperArgs struct {
	Value    string `json:"value"`
	PageSize int    `json:"pageSize"`
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
	papers, err := Search(args.Value, 1, args.PageSize, GetGlobalCookie())
	if err != nil {
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				mcp.NewTextContent(fmt.Sprintf("[ERROR] 搜索失败: %v", err)),
			},
			IsError: true,
		}, nil
	}

	// 循环获取影响因子并更新论文信息
	for i := range papers {
		if papers[i].SourceLink != "" {
			composite, comprehensive, err := GetSourceFactors(papers[i].SourceLink, GetGlobalCookie())
			if err != nil {
				continue
			} else {
				papers[i].SouceCompositeFactor = composite
				papers[i].SourceComprehensiveFactor = comprehensive
			}
			time.Sleep(100 * time.Millisecond)
		}
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
