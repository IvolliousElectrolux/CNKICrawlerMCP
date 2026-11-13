# CNKICrawlerMCP
![CNKICrawlerMCP](https://socialify.git.ci/Mangofang/CNKICrawlerMCP/image?custom_language=Go&font=Jost&forks=1&issues=1&language=1&logo=https%3A%2F%2Favatars.githubusercontent.com%2Fu%2F182288589%3Fs%3D200%26v%3D4&name=1&owner=1&pattern=Plus&stargazers=1&theme=Auto)
给 Cursor 用的知网 MCP. 连着校园网时可以检索论文, 查看关键词和摘要, 并下载 PDF.

## 功能

连着校园网即可使用, 不需要个人账号, 也不需要再打开机构登录页. 服务会自己做 IP 登录.

- 按主题检索论文
- 读取关键词和摘要
- 把 PDF 下载到本机 (机构订购范围内), 并按论文分文件夹
- 导出 Zotero 可导入的 `zotero.bib`, PDF 会作为附件挂上
- 把矢量 PDF 直接转成 `paper.html`, 文字和图片按原位置抽出, 不做 OCR
- 读取在线阅读正文, 以及期刊影响因子

离开校园网后 IP 登录会失败, 这时无法检索和下载.

## 安装

可执行文件由 GitHub Actions 编译, 不要在本机安装 Go 或本地编译. 推送到 GitHub 后打开 Actions 里的 `build`, 下载名为 `CNKICrawlerMCP` 的产物, 使用其中的 `CNKICrawlerMCP.exe`.

### 接入 Cursor

Cursor 的 MCP 要直接启动 exe, 不要套一层 `cmd /c`, 否则标准输出会被干扰, 工具调用会失败. 在 Cursor 的 MCP 配置里写成:

```json
{
  "mcpServers": {
    "CNKICrawlerMCP": {
      "command": "E:\\git-repo\\CNKICrawlerMCP\\CNKICrawlerMCP.exe"
    }
  }
}
```

把 `command` 换成 exe 的实际路径. 可选环境变量 `CNKI_DOWNLOAD_DIR` 用来指定文库根目录. 不设置时保存到用户目录下的 `Downloads\CNKI` (资源管理器里一般显示为下载).

每篇论文一个文件夹: `年份\题名\`. 里面是 `paper.pdf`, `paper.html` 和 `zotero.bib`. 图片在 `assets\`. `paper.html` 只提取 PDF 里已有的文字层和图片, 扫描件没有这些层时不会做 OCR. 在 Zotero 里选择 文件 - 导入, 打开 `zotero.bib`, PDF 和 HTML 会作为附件挂上. 若 `saveDir` 以 `.pdf` 结尾, 则 PDF 写到这个路径, bib 和 html 放在同一目录.

## 工具

1. `ScanPaper`: 主题检索, 返回题录和摘要页链接 `Href`.
2. `GetPaperInfo`: 用 `Href` 读取标题, 作者, 关键词和摘要.
3. `DownloadPaper`: 用 `Href` 下载 PDF, 同时写出 `zotero.bib` 和 `paper.html`, 并返回关键词, 摘要和这些路径. 不要自己去请求页面上的下载地址.
4. `GetPaperMainBody`: 用在线阅读链接提取正文.
5. `SetGlobalCookie` / `GetGlobalCookie`: 仅供调试. 校园网下不要手动塞 Cookie.

## 🏗️ 项目结构

```
CNKICrawlerMCP/
├── main.go              # MCP 服务器入口和工具定义
├── session.go           # 校园网 IP 登录和按域名保存的原始 Cookie
├── scanner.go           # 检索, 摘要, 关键词, 正文
├── download.go          # PDF 下载和按论文分目录
├── zotero.go            # Zotero BibTeX
├── pdfhtml.go           # 从 PDF 直接提取文字和图片, 写成 HTML
├── ulti.go              # 影响因子和 HTML 解析
├── types/
│   └── types.go         # 数据结构定义
├── go.mod               # Go模块定义
└── go.sum               # 依赖版本锁定
```

**免责声明**：本工具仅为技术研究目的开发，使用本工具产生的任何法律问题与开发者无关。请合理使用学术资源，尊重知识产权。
