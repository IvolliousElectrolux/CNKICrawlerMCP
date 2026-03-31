# CNKICrawlerMCP

一个基于ModelContextProtocol(MCP)的中国知网（CNKI）学术论文爬虫服务，为AI助手（如ClaudeDesktop、Cursor、CluadeCode等）提供论文检索、信息获取和正文内容提取能力。

## ✨ 功能特性

- **论文搜索**：通过关键词检索CNKI学术论文，返回标题、作者、来源等信息
- **论文详情**：获取论文的摘要、与正文（仅订阅套餐中允许查看的）
- **正文提取**：从HTML在线阅读页面提取论文正文内容（纯文本格式）
- **影响因子**：自动获取期刊的复合影响因子和综合影响因子

## 🛠️ 安装与运行

### 前提条件

- 需要登录并链接可用于IP登录的校园网环境
- Go 1.24.5 或更高版本（从源码构建时需要）

### 从源码构建

```bash
# 安装依赖
go mod download

# 构建可执行文件
go build
```

## 📚 使用方法

### 集成到Claude Code

在Claude Code的配置文件.claude.json中添加以下配置：

```json
{
	"CNKICrawlerMCP": {
	  "args": [
		"/c",
		"<EXE的绝对路径 如:D/CNKICrawlerMCP/CNKICrawlerMCP.exe>"
	  ],
	  "command": "cmd",
	  "env": {}
	}
}
```

### 集成到其他MCP客户端

参考对应客户端的MCP服务器配置文档，将CNKICrawlerMCP作为外部工具添加。

## 🛠️ 可用工具

### 1. ScanPaper - 论文检索

通过关键词检索CNKI学术论文。

### 2. GetPaperInfo - 获取论文信息

通过ScanPaper获取的论文链接，获取详细论文信息。

### 3. GetPaperMainBody - 获取论文正文

通过GetPaperInfo获取的HTML在线阅读链接，提取论文正文内容。

## 🏗️ 项目结构

```
CNKICrawlerMCP/
├── main.go              # MCP服务器主入口，工具定义和路由
├── scanner.go           # 核心爬虫逻辑：搜索、信息获取、正文提取
├── ulti.go              # 工具函数：影响因子提取、HTML解析、登录逻辑
├── globalCookie.go      # 全局Cookie管理
├── types/
│   └── types.go         # 数据结构定义
├── go.mod               # Go模块定义
└── go.sum               # 依赖版本锁定
```

## ⚠️ 注意事项

1. **使用限制**：当前仅支持IP登录，需要先连接订阅单位的网络后使用

**免责声明**：本工具仅为技术研究目的开发，使用本工具产生的任何法律问题与开发者无关。请合理使用学术资源，尊重知识产权。