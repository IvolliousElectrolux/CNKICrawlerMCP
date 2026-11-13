package main

import (
	"CNKICrawlerMCP/types"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var authorSuffixRe = regexp.MustCompile(`[\d,，、\s]+$`)

// locatePaper 决定这篇论文的目录和 PDF 路径.
// 目录保存时按 年份/题名 分开放, 同一题名但是另一篇链接时在目录名后加短哈希.
// saveDir 以 .pdf 结尾时, PDF 用这个路径, bib 和 html 写在同一目录.
func locatePaper(saveDir string, info types.PaperInfo) (dir, pdfPath string, err error) {
	target := strings.TrimSpace(saveDir)
	if target == "" {
		if env := strings.TrimSpace(os.Getenv("CNKI_DOWNLOAD_DIR")); env != "" {
			target = env
		} else {
			home, homeErr := os.UserHomeDir()
			if homeErr != nil {
				return "", "", homeErr
			}
			target = filepath.Join(home, "Downloads", "CNKI")
		}
	}
	if strings.EqualFold(filepath.Ext(target), ".pdf") {
		dir = filepath.Dir(target)
		if err = os.MkdirAll(dir, 0o755); err != nil {
			return "", "", err
		}
		return dir, absPath(target), nil
	}
	year := "未知年份"
	if info.Year > 0 {
		year = strconv.Itoa(info.Year)
	}
	stem := strings.TrimSuffix(safePDFName(info.Title), ".pdf")
	dir = filepath.Join(target, year, stem)
	if folderTakenByOther(dir, info.URL) {
		dir = filepath.Join(target, year, stem+"-"+shortHash(info.URL))
	}
	if err = os.MkdirAll(dir, 0o755); err != nil {
		return "", "", err
	}
	return dir, absPath(filepath.Join(dir, "paper.pdf")), nil
}

func folderTakenByOther(dir, href string) bool {
	b, err := os.ReadFile(filepath.Join(dir, "zotero.bib"))
	if err != nil || href == "" {
		return false
	}
	return !strings.Contains(string(b), "url = {"+bibText(href)+"}")
}

func shortHash(s string) string {
	sum := sha1.Sum([]byte(s))
	return hex.EncodeToString(sum[:4])
}

func citeKey(info types.PaperInfo) string {
	sum := sha1.Sum([]byte(info.URL + "\n" + info.Title))
	id := hex.EncodeToString(sum[:4])
	if info.Year > 0 {
		return fmt.Sprintf("cnki%d%s", info.Year, id)
	}
	return "cnki" + id
}

func cleanAuthor(name string) string {
	name = strings.TrimSpace(name)
	cleaned := strings.TrimSpace(authorSuffixRe.ReplaceAllString(name, ""))
	if cleaned == "" {
		return name
	}
	return cleaned
}

func bibText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, "{", `\{`)
	s = strings.ReplaceAll(s, "}", `\}`)
	return strings.Join(strings.Fields(s), " ")
}

func bibPages(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	p = strings.NewReplacer("—", "-", "–", "-", "~", "-", "～", "-", " ", "").Replace(p)
	p = strings.ReplaceAll(p, "--", "-")
	return strings.ReplaceAll(p, "-", "--")
}

// zoteroPath 把 Windows 路径写成 Zotero 能挂上附件的 file 字段.
// 盘符冒号要转义, 反斜杠要写成两个, 否则导入时会把 C: 当成分隔符丢掉附件.
func zoteroPath(p string) string {
	p = filepath.Clean(p)
	var b strings.Builder
	for i, r := range p {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case ':':
			if i == 1 {
				b.WriteString(`\:`)
				continue
			}
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func zoteroFile(desc, path, mime string) string {
	return bibText(desc) + ":" + zoteroPath(path) + ":" + mime
}

func writeField(b *strings.Builder, key, val string) {
	val = bibText(val)
	if val == "" {
		return
	}
	fmt.Fprintf(b, "  %s = {%s},\n", key, val)
}

// writeZoteroBib 写 UTF-8 BibTeX. Zotero 用 文件 - 导入 打开后, file 字段里的 PDF 会成为附件.
func writeZoteroBib(dir string, info types.PaperInfo, pdfPath, htmlPath string) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "@article{%s,\n", citeKey(info))
	writeField(&b, "title", info.Title)
	if len(info.Authors) > 0 {
		names := make([]string, 0, len(info.Authors))
		for _, name := range info.Authors {
			if name = cleanAuthor(name); name != "" {
				names = append(names, bibText(name))
			}
		}
		if len(names) > 0 {
			fmt.Fprintf(&b, "  author = {%s},\n", strings.Join(names, " and "))
		}
	}
	writeField(&b, "journal", info.Journal)
	if info.Year > 0 {
		fmt.Fprintf(&b, "  year = {%d},\n", info.Year)
	}
	writeField(&b, "volume", info.Volume)
	writeField(&b, "number", info.Issue)
	if pages := bibPages(info.Pages); pages != "" {
		fmt.Fprintf(&b, "  pages = {%s},\n", pages)
	}
	writeField(&b, "doi", info.DOI)
	writeField(&b, "url", info.URL)
	writeField(&b, "abstract", info.Abstract)
	if len(info.Keywords) > 0 {
		writeField(&b, "keywords", strings.Join(info.Keywords, ", "))
	}
	writeField(&b, "language", "zh-CN")
	if info.OnlineFirst {
		writeField(&b, "note", "网络首发")
	}
	var files []string
	if pdfPath != "" {
		files = append(files, zoteroFile(filepath.Base(pdfPath), pdfPath, "application/pdf"))
	}
	if htmlPath != "" {
		files = append(files, zoteroFile(filepath.Base(htmlPath), htmlPath, "text/html"))
	}
	if len(files) > 0 {
		fmt.Fprintf(&b, "  file = {%s},\n", strings.Join(files, ";"))
	}
	b.WriteString("}\n")
	path := filepath.Join(dir, "zotero.bib")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return "", err
	}
	return absPath(path), nil
}
