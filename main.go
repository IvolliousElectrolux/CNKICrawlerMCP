package main

import (
	"CNKICrawlerMCP/types"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

const serverInstructions = `校园网 IP 自动登录的知网工具, 不需要用户提供账号, 机构登录或 Cookie.
连接校园网时直接调用工具. 只有用户明确粘贴了 Cookie 才使用 SetGlobalCookie.
1. ScanPaper 检索题录.
2. GetPaperInfo 查看摘要和关键词.
3. DownloadPaper 把 PDF 存到本机, 并在同一文件夹写出 zotero.bib 和 paper.html. paper.html 直接提取文字和图片, 不做 OCR. 在 Zotero 里选择 文件 - 导入 打开 zotero.bib.
遇到依次点击文字的验证时, 服务会自己识别位置并提交, 不需要用户手动点.
不要自己请求 bar.cnki.net, docdown.cnki.net 或返回结果里的 PDF 链接, 那些地址离开本服务的会话会失败.`

func main() {
	log.SetOutput(os.Stderr)
	s := server.NewMCPServer("CNKICrawlerMcp", "1.2.0", server.WithInstructions(serverInstructions))
	s.AddTool(mcp.Tool{
		Name:        "ScanPaper",
		Description: "在校园网下按主题检索知网. 自动 IP 登录, 不需要 Cookie. 返回标题, 作者, 来源, 发表年份, 摘要页链接 Href. 要摘要和关键词再调 GetPaperInfo, 要 PDF 再把 Href 传给 DownloadPaper",
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
		Description: "用 ScanPaper 返回的 Href 读取标题, 作者, 关键词, 摘要和发表年份. HasPDF 表示这篇在当前校园网下有 PDF 按钮. 不下载文件, 下载请调用 DownloadPaper",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"href": map[string]string{"type": "string", "description": "论文链接"},
			},
			Required: []string{"href"},
		},
	}, handleGetPaperInfo)
	s.AddTool(mcp.Tool{
		Name:        "DownloadPaper",
		Description: "用 ScanPaper 或 GetPaperInfo 的论文 Href 下载 PDF. 校园网 IP 自动登录, 不需要个人账号. 每篇论文一个文件夹, 内有 paper.pdf, paper.html 和 zotero.bib. paper.html 直接提取文字和图片 (含图文位置), 不做 OCR. 成功时返回这些路径, 标题, 关键词和摘要. 不要改用浏览器或 curl 去打开 PDF 链接",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"href":    map[string]string{"type": "string", "description": "ScanPaper 返回的论文摘要页链接 Href"},
				"saveDir": map[string]string{"type": "string", "description": "可选. 文库根目录, 或以 .pdf 结尾的完整路径. 默认是用户下载目录下的 CNKI. 目录模式下按 年份/题名 分文件夹"},
			},
			Required: []string{"href"},
		},
	}, handleDownloadPaper)
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
		Description: "仅当用户明确粘贴了浏览器 Cookie 时才调用. 校园网下不要调用, 服务会自动 IP 登录",
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
		Description: "调试用: 查看当前发给 kns.cnki.net 的 Cookie. 不要拿它去自行下载 PDF",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
		},
	}, handleGetGlobalCookie)

	if err := server.ServeStdio(s); err != nil {
		log.Printf("MCP 退出: %v", err)
	}
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
	papers, err := Search(value, pageNum, pageSize)
	if errors.Is(err, ErrCookieExpired) {
		if ok, loginErr := RefreshLogin(); ok && loginErr == nil {
			return Search(value, pageNum, pageSize)
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
		composite, comprehensive, err := GetSourceFactors(link)
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
	paperInfo, err := GetPaperInfo(args.Href)
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
	paperMainBody, err := GetPaperMainBody(args.Href)
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
