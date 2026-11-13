package main

import (
	"bytes"
	"compress/flate"
	"compress/zlib"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"html"
	"image"
	"image/png"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf16"

	"github.com/ledongthuc/pdf"
	"github.com/robert-mcdermott/jp2k"
	"golang.org/x/text/encoding/simplifiedchinese"
)

const htmlScale = 96.0 / 72.0

type htmlExport struct {
	Path      string
	Images    int
	TextChars int
}

type mat3 [3][3]float64

func matIdent() mat3 {
	return mat3{{1, 0, 0}, {0, 1, 0}, {0, 0, 1}}
}

func matFrom(a, b, c, d, e, f float64) mat3 {
	return mat3{{a, b, 0}, {c, d, 0}, {e, f, 1}}
}

func (m mat3) mul(n mat3) mat3 {
	var z mat3
	for i := 0; i < 3; i++ {
		for j := 0; j < 3; j++ {
			z[i][j] = m[i][0]*n[0][j] + m[i][1]*n[1][j] + m[i][2]*n[2][j]
		}
	}
	return z
}

func (m mat3) apply(x, y float64) (float64, float64) {
	return x*m[0][0] + y*m[1][0] + m[2][0], x*m[0][1] + y*m[1][1] + m[2][1]
}

type glyph struct {
	x, y, size, w float64
	s             string
	face          string
	line          float64
}

type placedImg struct {
	ctm mat3
	obj pdf.Value
}

type rawImage struct {
	w, h, length int
	data         []byte
	used         bool
}

var (
	reWidth  = regexp.MustCompile(`/Width\s+(\d+)`)
	reHeight = regexp.MustCompile(`/Height\s+(\d+)`)
	reLength = regexp.MustCompile(`/Length\s+(\d+)(?:\s+(\d+)\s+R)?`)
	reIntObj = regexp.MustCompile(`(\d+)\s+\d+\s+obj\s+(\d+)\s+endobj`)
)

// exportPDFHTML 把矢量 PDF 的文字和图片直接抽成 HTML, 不做 OCR.
// 图片按出现位置摆进每一页, 文字按阅读顺序写进 DOM, 方便索引.
func exportPDFHTML(pdfBytes []byte, dir, title string) (out htmlExport, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("解析 PDF 时中断: %v", r)
		}
	}()
	// 知网下载的 PDF 在 %%EOF 之后还会附一段 FileProperty XML.
	// 解析库只检查文件末尾, 这段尾巴会让它认为文件不是 PDF.
	pdfBytes = trimCNKIPDF(pdfBytes)
	r, err := pdf.NewReader(bytes.NewReader(pdfBytes), int64(len(pdfBytes)))
	if err != nil {
		return out, err
	}
	rawImages := indexRawImages(pdfBytes)
	book := &fontBook{byPtr: map[*pdf.Font]*fontSlot{}, bySum: map[string]*embeddedFace{}}
	n := r.NumPage()
	if n > 500 {
		n = 500
	}

	var body strings.Builder
	if strings.TrimSpace(title) != "" {
		fmt.Fprintf(&body, "<h1>%s</h1>\n", html.EscapeString(strings.TrimSpace(title)))
	}
	assetDir := filepath.Join(dir, "assets")
	for i := 1; i <= n; i++ {
		page := r.Page(i)
		if page.V.IsNull() {
			continue
		}
		pw, ph, ox, oy := pageBox(page)
		var glyphs []glyph
		var images []placedImg
		contents := page.V.Key("Contents")
		if contents.Kind() != pdf.Null {
			walkStream(contents, page.Resources(), matIdent(), 0, &glyphs, &images, book)
		}
		boxes := make([]htmlBox, 0, len(glyphs)+len(images))
		for _, run := range groupGlyphs(glyphs) {
			text := strings.TrimSpace(run.s)
			if text == "" {
				continue
			}
			// 个别字形的坐标落在页面外很远, 多半是字体里的废点, 画出来会把行宽拉飞.
			if run.x < ox-36 || run.x > ox+pw+36 || run.y < oy-36 || run.y > oy+ph+36 {
				continue
			}
			out.TextChars += len([]rune(text))
			size := run.size
			if size < 1 {
				size = 10
			}
			left := (run.x - ox) * htmlScale
			top := (oy + ph - run.y - size*0.8) * htmlScale
			line := run.line
			if line == 0 {
				line = run.y
			}
			// 浏览器字体比知网里的 Times 宽, 整行会冲出页边. 按 PDF 里的字宽把这一段压回去.
			fs := size * htmlScale
			width := run.w * htmlScale
			if width < fs*0.4 {
				width = fs * 0.5 * float64(len([]rune(run.s)))
			}
			height := fs * 1.35
			faceStyle := ""
			if run.face != "" {
				faceStyle = fmt.Sprintf(` style="font-family:'%s',SimSun,serif"`, run.face)
			}
			boxes = append(boxes, htmlBox{line, fmt.Sprintf(
				`<svg class="run" style="left:%.1fpx;top:%.1fpx;width:%.1fpx;height:%.1fpx" viewBox="0 0 %.2f %.2f"><text%s xml:space="preserve" x="0" y="%.2f" font-size="%.2f" textLength="%.2f" lengthAdjust="spacingAndGlyphs">%s</text></svg>`,
				left, top, width, height, width, height, faceStyle, fs*0.8, fs, width, html.EscapeString(run.s),
			)})
		}
		for k, im := range images {
			minX, minY, maxX, maxY := unitBox(im.ctm)
			left := (minX - ox) * htmlScale
			top := (oy + ph - maxY) * htmlScale
			width := (maxX - minX) * htmlScale
			height := (maxY - minY) * htmlScale
			if width < 2 || height < 2 {
				continue
			}
			// 盖住整页的是页面底图, 不是插图. 留下的话正文就不成文字了.
			if pw > 0 && ph > 0 && (maxX-minX) >= pw*0.85 && (maxY-minY) >= ph*0.85 {
				continue
			}
			ext, data := imageFile(im.obj, rawImages)
			if len(data) == 0 || ext == "" {
				continue
			}
			if err := os.MkdirAll(assetDir, 0o755); err != nil {
				return out, err
			}
			name := fmt.Sprintf("p%d-%d.%s", i, k+1, ext)
			if err := os.WriteFile(filepath.Join(assetDir, name), data, 0o644); err != nil {
				return out, err
			}
			out.Images++
			alt := fmt.Sprintf("第 %d 页图片", i)
			boxes = append(boxes, htmlBox{maxY, fmt.Sprintf(
				`<img alt="%s" src="assets/%s" style="left:%.1fpx;top:%.1fpx;width:%.1fpx;height:%.1fpx;%s">`,
				html.EscapeString(alt), name, left, top, width, height, imageTurn(im.ctm),
			)})
		}
		sort.SliceStable(boxes, func(a, b int) bool { return boxes[a].line > boxes[b].line })
		fmt.Fprintf(&body, `<div class="page" data-page="%d" style="width:%.1fpx;height:%.1fpx">`+"\n", i, pw*htmlScale, ph*htmlScale)
		for _, box := range boxes {
			body.WriteString(box.html)
			body.WriteByte('\n')
		}
		body.WriteString("</div>\n")
	}
	if out.TextChars < 20 && out.Images == 0 {
		body.WriteString(`<p class="note">这份 PDF 没有可直接提取的文字或图片层. 未做 OCR.</p>` + "\n")
	}
	var doc strings.Builder
	doc.WriteString("<!DOCTYPE html>\n<html lang=\"zh-CN\">\n<head>\n<meta charset=\"utf-8\">\n<title>")
	doc.WriteString(html.EscapeString(strings.TrimSpace(title)))
	doc.WriteString("</title>\n<style>\n")
	doc.WriteString("body{margin:0;background:#f3f3f3;color:#111;font-family:\"Times New Roman\",\"Source Han Serif SC\",\"Noto Serif CJK SC\",SimSun,serif}\n")
	doc.WriteString("h1{font-size:22px;font-weight:600;margin:24px auto 8px;max-width:920px;padding:0 16px}\n")
	doc.WriteString(".page{position:relative;margin:16px auto;background:#fff;box-shadow:0 1px 4px rgba(0,0,0,.15);overflow:hidden}\n")
	doc.WriteString(".page svg.run{position:absolute;overflow:visible}\n")
	doc.WriteString(".page svg.run text{font-family:\"Times New Roman\",\"Source Han Serif SC\",\"Noto Serif CJK SC\",SimSun,serif;fill:#111}\n")
	doc.WriteString(book.css())
	doc.WriteString(".page img{position:absolute}\n")
	doc.WriteString(".note{max-width:920px;margin:12px auto;padding:0 16px;font:14px sans-serif;color:#666}\n")
	doc.WriteString("</style>\n</head>\n<body>\n")
	doc.WriteString(body.String())
	doc.WriteString("</body>\n</html>\n")
	path := filepath.Join(dir, "paper.html")
	if err := os.WriteFile(path, []byte(doc.String()), 0o644); err != nil {
		return out, err
	}
	out.Path = absPath(path)
	return out, nil
}

type htmlBox struct {
	line float64
	html string
}

// trimCNKIPDF 去掉 %%EOF 之后知网附加的元数据, 不改落盘的 PDF.
func trimCNKIPDF(data []byte) []byte {
	i := bytes.LastIndex(data, []byte("%%EOF"))
	if i < 0 {
		return data
	}
	end := i + len("%%EOF")
	if end < len(data) && (data[end] == '\n' || data[end] == '\r') {
		end++
		if end < len(data) && data[end-1] == '\r' && data[end] == '\n' {
			end++
		}
	}
	if end >= len(data) {
		return data
	}
	return data[:end]
}

func pageBox(p pdf.Page) (w, h, ox, oy float64) {
	v := p.V
	for !v.IsNull() {
		box := v.Key("MediaBox")
		if box.Kind() != pdf.Array || box.Len() < 4 {
			box = v.Key("CropBox")
		}
		if box.Kind() == pdf.Array && box.Len() >= 4 {
			x0 := box.Index(0).Float64()
			y0 := box.Index(1).Float64()
			x1 := box.Index(2).Float64()
			y1 := box.Index(3).Float64()
			if x1 < x0 {
				x0, x1 = x1, x0
			}
			if y1 < y0 {
				y0, y1 = y1, y0
			}
			if x1 > x0 && y1 > y0 {
				return x1 - x0, y1 - y0, x0, y0
			}
		}
		v = v.Key("Parent")
	}
	return 595.28, 841.89, 0, 0
}

func unitBox(m mat3) (minX, minY, maxX, maxY float64) {
	pts := [4][2]float64{{0, 0}, {1, 0}, {1, 1}, {0, 1}}
	minX, minY = m.apply(pts[0][0], pts[0][1])
	maxX, maxY = minX, minY
	for _, p := range pts[1:] {
		x, y := m.apply(p[0], p[1])
		if x < minX {
			minX = x
		}
		if x > maxX {
			maxX = x
		}
		if y < minY {
			minY = y
		}
		if y > maxY {
			maxY = y
		}
	}
	return minX, minY, maxX, maxY
}

// imageTurn 按图片矩阵把倒贴的图翻正. 横轴 a, 纵轴 d 为负时, PDF 里看到的是翻过来的.
func imageTurn(m mat3) string {
	flipX := m[0][0] < 0
	flipY := m[1][1] < 0
	switch {
	case flipX && flipY:
		return "transform:scale(-1,-1)"
	case flipY:
		return "transform:scaleY(-1)"
	case flipX:
		return "transform:scaleX(-1)"
	default:
		return ""
	}
}

type textState struct {
	tc, tw, th, tl, tfs, trise float64
	tm, tlm, ctm               mat3
	font                       *pdf.Font
	enc                        pdf.TextEncoding
	metric                     fontMetric
}

// fontMetric 是知网字体里真正能用的字宽.
// 很多 CID 字体没有 Widths, 只给 /DW 1000, 不按这个推进的话同一串字会叠在一个点上, 排序后相邻汉字就会颠倒.
type fontMetric struct {
	codeBytes int
	dw        float64
	exact     map[int]float64
	spans     []widthSpan
	simple    bool
	encoding  string
	gbk       bool
	uni       map[int]string
}

type widthSpan struct {
	lo, hi int
	w      float64
}

func (m fontMetric) width(code int) float64 {
	if w, ok := m.exact[code]; ok && w > 0 {
		return w
	}
	for _, s := range m.spans {
		if code >= s.lo && code <= s.hi && s.w > 0 {
			return s.w
		}
	}
	if m.dw > 0 {
		return m.dw
	}
	return 0
}

func metricFor(f *pdf.Font) (m fontMetric) {
	defer func() { recover() }()
	if f == nil {
		return fontMetric{codeBytes: 1, simple: true}
	}
	m.encoding = f.V.Key("Encoding").Name()
	m.gbk = strings.Contains(m.encoding, "GBK") || strings.Contains(m.encoding, "GB-EUC") || strings.Contains(m.encoding, "GBpc")
	m.uni = unicodeMap(f)
	desc := f.V.Key("DescendantFonts")
	if desc.Kind() != pdf.Array || desc.Len() == 0 {
		m.codeBytes = 1
		m.simple = true
		return m
	}
	m.codeBytes = 2
	m.dw = 1000
	cid := desc.Index(0)
	if dw := cid.Key("DW").Float64(); dw > 0 {
		m.dw = dw
	}
	w := cid.Key("W")
	if w.Kind() != pdf.Array {
		return m
	}
	for i := 0; i < w.Len(); {
		start := int(w.Index(i).Int64())
		i++
		if i >= w.Len() {
			break
		}
		nxt := w.Index(i)
		if nxt.Kind() == pdf.Array {
			if m.exact == nil {
				m.exact = map[int]float64{}
			}
			for j := 0; j < nxt.Len(); j++ {
				m.exact[start+j] = nxt.Index(j).Float64()
			}
			i++
			continue
		}
		end := int(nxt.Int64())
		i++
		if i >= w.Len() {
			break
		}
		width := w.Index(i).Float64()
		i++
		if end < start || end-start > 65535 {
			continue
		}
		m.spans = append(m.spans, widthSpan{start, end, width})
	}
	return m
}

// stepAt 返回 raw[i] 这个字占用的字节数. Identity-H 固定 2 字节, GBK-EUC-H 的 ASCII 是 1 字节.
func (m fontMetric) stepAt(raw string, i int) int {
	if m.codeBytes < 2 || i >= len(raw) {
		return 1
	}
	if strings.Contains(m.encoding, "GBK") || strings.Contains(m.encoding, "EUC") {
		if raw[i] < 0x80 || i+1 >= len(raw) {
			return 1
		}
	}
	if i+1 >= len(raw) {
		return 1
	}
	return 2
}

// decodeGlyph 优先用字体自己的 ToUnicode. 方正 GBK 字体没有这张表, 字符码就是 GBK.
// 解析库遇到 GBK-EUC-H 会原样返回字节, 再按 UTF-8 读就成了替换符.
func decodeGlyph(g textState, chunk string, code int) string {
	if g.metric.gbk {
		decoded, err := simplifiedchinese.GBK.NewDecoder().Bytes([]byte(chunk))
		if err != nil || len(decoded) == 0 {
			return ""
		}
		return string(decoded)
	}
	if len(g.metric.uni) > 0 && !strings.Contains(g.metric.encoding, "Identity") {
		if s, ok := g.metric.uni[code]; ok {
			return s
		}
		return ""
	}
	if g.enc == nil {
		return ""
	}
	return g.enc.Decode(chunk)
}

func unicodeMap(f *pdf.Font) map[int]string {
	if f == nil {
		return nil
	}
	v := f.V.Key("ToUnicode")
	if v.Kind() != pdf.Stream {
		return nil
	}
	rc := v.Reader()
	if rc == nil {
		return nil
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil || len(data) == 0 {
		return nil
	}
	return parseToUnicode(string(data))
}

var (
	reCMapBlock = regexp.MustCompile(`(?s)beginbfchar(.*?)endbfchar|beginbfrange(.*?)endbfrange`)
	reCMapHex   = regexp.MustCompile(`<([0-9A-Fa-f]+)>`)
)

func parseToUnicode(s string) map[int]string {
	out := map[int]string{}
	for _, m := range reCMapBlock.FindAllStringSubmatch(s, -1) {
		body := m[1]
		isRange := false
		if body == "" {
			body = m[2]
			isRange = true
		}
		if !isRange {
			hs := reCMapHex.FindAllStringSubmatch(body, -1)
			for i := 0; i+1 < len(hs); i += 2 {
				out[hexInt(hs[i][1])] = decodeUTF16Hex(hs[i+1][1])
			}
			continue
		}
		for _, line := range strings.FieldsFunc(body, func(r rune) bool { return r == '\n' || r == '\r' }) {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "%") || strings.Contains(line, "[") {
				continue
			}
			hs := reCMapHex.FindAllStringSubmatch(line, -1)
			if len(hs) < 3 {
				continue
			}
			lo, hi := hexInt(hs[0][1]), hexInt(hs[1][1])
			if hi < lo || hi-lo > 65535 {
				continue
			}
			base := decodeUTF16Hex(hs[2][1])
			for code := lo; code <= hi; code++ {
				out[code] = shiftLastRune(base, code-lo)
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func hexInt(s string) int {
	n := 0
	for _, c := range s {
		n *= 16
		switch {
		case c >= '0' && c <= '9':
			n += int(c - '0')
		case c >= 'a' && c <= 'f':
			n += int(c-'a') + 10
		case c >= 'A' && c <= 'F':
			n += int(c-'A') + 10
		}
	}
	return n
}

func decodeUTF16Hex(h string) string {
	if len(h)%2 == 1 {
		h = "0" + h
	}
	raw := make([]byte, len(h)/2)
	for i := range raw {
		raw[i] = byte(hexInt(h[i*2 : i*2+2]))
	}
	if len(raw) < 2 {
		if len(raw) == 1 {
			return string(rune(raw[0]))
		}
		return ""
	}
	u := make([]uint16, len(raw)/2)
	for i := range u {
		u[i] = uint16(raw[i*2])<<8 | uint16(raw[i*2+1])
	}
	return string(utf16.Decode(u))
}

func shiftLastRune(s string, delta int) string {
	if delta == 0 {
		return s
	}
	rs := []rune(s)
	if len(rs) == 0 {
		return ""
	}
	rs[len(rs)-1] += rune(delta)
	return string(rs)
}

func walkStream(strm, resources pdf.Value, ctm mat3, depth int, glyphs *[]glyph, images *[]placedImg, book *fontBook) {
	if depth > 6 || strm.IsNull() {
		return
	}
	defer func() { recover() }()
	g := textState{th: 1, ctm: ctm}
	var stack []textState
	fonts := map[string]*pdf.Font{}
	metrics := map[*pdf.Font]fontMetric{}
	show := func(raw string) {
		if g.enc == nil || g.font == nil || raw == "" {
			return
		}
		for i := 0; i < len(raw); {
			n := g.metric.stepAt(raw, i)
			chunk := raw[i : i+n]
			i += n
			code := int(chunk[0])
			if n == 2 {
				code = int(chunk[0])<<8 | int(chunk[1])
			}
			text := decodeGlyph(g, chunk, code)
			if text == "" {
				continue
			}
			w0 := g.metric.width(code)
			if g.metric.simple {
				if sw := g.font.Width(code); sw > 0 {
					w0 = sw
				}
			}
			if w0 <= 0 {
				// 知网有的 TrueType 只写了空格的宽度, 其余字形按半字宽往右排, 避免叠在一起后被排序颠倒.
				w0 = 500
				if !g.metric.simple && g.metric.dw > 0 {
					w0 = g.metric.dw
				}
			}
			trm := matFrom(g.tfs*g.th, 0, 0, g.tfs, 0, g.trise).mul(g.tm).mul(g.ctm)
			x, y := trm[2][0], trm[2][1]
			size := math.Hypot(trm[1][0], trm[1][1])
			if size < 0.1 {
				size = math.Abs(g.tfs)
			}
			adv := w0 / 1000 * math.Abs(trm[0][0])
			if adv == 0 {
				adv = size * 0.5
			}
			for _, ch := range text {
				if ch < 32 || ch == '\uFFFD' || (ch >= 0xE000 && ch <= 0xF8FF) {
					continue
				}
				face := ""
				if book != nil {
					face = book.use(g.font, code, ch)
				}
				*glyphs = append(*glyphs, glyph{x: x, y: y, size: size, w: adv, s: string(ch), face: face})
			}
			tx := w0/1000*g.tfs + g.tc
			if strings.ContainsRune(text, ' ') {
				tx += g.tw
			}
			tx *= g.th
			g.tm = matFrom(1, 0, 0, 1, tx, 0).mul(g.tm)
		}
	}
	pdf.Interpret(strm, func(stk *pdf.Stack, op string) {
		args := popAll(stk)
		switch op {
		case "q":
			stack = append(stack, g)
		case "Q":
			if n := len(stack); n > 0 {
				g = stack[n-1]
				stack = stack[:n-1]
			}
		case "cm":
			if len(args) != 6 {
				return
			}
			m := matFrom(args[0].Float64(), args[1].Float64(), args[2].Float64(), args[3].Float64(), args[4].Float64(), args[5].Float64())
			g.ctm = m.mul(g.ctm)
		case "Do":
			if len(args) < 1 {
				return
			}
			obj := resources.Key("XObject").Key(args[0].Name())
			if obj.IsNull() || obj.Key("ImageMask").Bool() {
				return
			}
			switch obj.Key("Subtype").Name() {
			case "Image":
				if obj.Key("Width").Int64() < 8 || obj.Key("Height").Int64() < 8 {
					return
				}
				*images = append(*images, placedImg{ctm: g.ctm, obj: obj})
			case "Form":
				fm := matIdent()
				matrix := obj.Key("Matrix")
				if matrix.Kind() == pdf.Array && matrix.Len() >= 6 {
					fm = matFrom(matrix.Index(0).Float64(), matrix.Index(1).Float64(), matrix.Index(2).Float64(), matrix.Index(3).Float64(), matrix.Index(4).Float64(), matrix.Index(5).Float64())
				}
				res := obj.Key("Resources")
				if res.IsNull() {
					res = resources
				}
				walkStream(obj, res, fm.mul(g.ctm), depth+1, glyphs, images, book)
			}
		case "BT":
			g.tm = matIdent()
			g.tlm = g.tm
		case "T*":
			x := matFrom(1, 0, 0, 1, 0, -g.tl)
			g.tlm = x.mul(g.tlm)
			g.tm = g.tlm
		case "Tc":
			if len(args) == 1 {
				g.tc = args[0].Float64()
			}
		case "TD":
			if len(args) == 2 {
				g.tl = -args[1].Float64()
				tx, ty := args[0].Float64(), args[1].Float64()
				x := matFrom(1, 0, 0, 1, tx, ty)
				g.tlm = x.mul(g.tlm)
				g.tm = g.tlm
			}
		case "Td":
			if len(args) == 2 {
				x := matFrom(1, 0, 0, 1, args[0].Float64(), args[1].Float64())
				g.tlm = x.mul(g.tlm)
				g.tm = g.tlm
			}
		case "Tf":
			if len(args) != 2 {
				return
			}
			name := args[0].Name()
			f := fonts[name]
			if f == nil {
				f = &pdf.Font{V: resources.Key("Font").Key(name)}
				fonts[name] = f
			}
			g.font = f
			g.enc = safeEncoder(f)
			if met, ok := metrics[f]; ok {
				g.metric = met
			} else {
				g.metric = metricFor(f)
				metrics[f] = g.metric
			}
			g.tfs = args[1].Float64()
		case "Tj":
			if len(args) == 1 {
				show(args[0].RawString())
			}
		case "TJ":
			if len(args) != 1 {
				return
			}
			v := args[0]
			for i := 0; i < v.Len(); i++ {
				x := v.Index(i)
				if x.Kind() == pdf.String {
					show(x.RawString())
				} else {
					tx := -x.Float64() / 1000 * g.tfs * g.th
					g.tm = matFrom(1, 0, 0, 1, tx, 0).mul(g.tm)
				}
			}
		case "'":
			if len(args) != 1 {
				return
			}
			x := matFrom(1, 0, 0, 1, 0, -g.tl)
			g.tlm = x.mul(g.tlm)
			g.tm = g.tlm
			show(args[0].RawString())
		case "\"":
			if len(args) != 3 {
				return
			}
			g.tw = args[0].Float64()
			g.tc = args[1].Float64()
			x := matFrom(1, 0, 0, 1, 0, -g.tl)
			g.tlm = x.mul(g.tlm)
			g.tm = g.tlm
			show(args[2].RawString())
		case "Tw":
			if len(args) == 1 {
				g.tw = args[0].Float64()
			}
		case "TL":
			if len(args) == 1 {
				g.tl = args[0].Float64()
			}
		case "Tm":
			if len(args) != 6 {
				return
			}
			m := matFrom(args[0].Float64(), args[1].Float64(), args[2].Float64(), args[3].Float64(), args[4].Float64(), args[5].Float64())
			g.tm = m
			g.tlm = m
		case "Ts":
			if len(args) == 1 {
				g.trise = args[0].Float64()
			}
		case "Tz":
			if len(args) == 1 {
				g.th = args[0].Float64() / 100
			}
		}
	})
}

func popAll(stk *pdf.Stack) []pdf.Value {
	n := stk.Len()
	args := make([]pdf.Value, n)
	for i := n - 1; i >= 0; i-- {
		args[i] = stk.Pop()
	}
	return args
}

func safeEncoder(f *pdf.Font) (enc pdf.TextEncoding) {
	defer func() { recover() }()
	if f == nil {
		return nil
	}
	return f.Encoder()
}

func groupGlyphs(gs []glyph) []glyph {
	if len(gs) == 0 {
		return nil
	}
	// 同一页有时会叠两层相同的字. 位置几乎重合的同一个字只留一个.
	kept := gs[:0:0]
	for _, g := range gs {
		dup := false
		for i := range kept {
			k := kept[i]
			if k.s != g.s || math.Abs(k.y-g.y) > math.Max(k.size, g.size)*0.35 {
				continue
			}
			if math.Abs(k.x-g.x) < math.Max(k.size, g.size)*0.15 {
				dup = true
				break
			}
		}
		if !dup {
			kept = append(kept, g)
		}
	}
	gs = kept
	sort.SliceStable(gs, func(i, j int) bool { return gs[i].y > gs[j].y })
	// 先按较宽的基线把上标和正文圈进同一带, 再把明显更小的上标拆出去.
	// 知网的 [1,4,7] 比正文高大约 3pt, 直接按横坐标排会插进 "通过" 这种词中间.
	var body, notes []glyph
	for _, line := range clusterByY(gs, 0.55) {
		bs := upperQuartileSize(line)
		for _, g := range line {
			if bs > 2 && g.size < bs*0.78 && g.size < bs-1.2 {
				notes = append(notes, g)
				continue
			}
			body = append(body, g)
		}
	}
	sort.SliceStable(body, func(i, j int) bool { return body[i].y > body[j].y })
	type run struct {
		glyphs   []glyph
		y, size  float64
	}
	var runs []run
	for _, line := range clusterByY(body, 0.32) {
		baseY := line[0].y
		sort.SliceStable(line, func(i, j int) bool { return line[i].x < line[j].x })
		var cur []glyph
		flush := func() {
			if len(cur) == 0 {
				return
			}
			runs = append(runs, run{glyphs: cur, y: baseY, size: cur[0].size})
			cur = nil
		}
		for _, g := range line {
			if len(cur) == 0 {
				cur = []glyph{g}
				continue
			}
			last := cur[len(cur)-1]
			pitch := g.x - last.x
			limit := math.Max(last.size, g.size) * 1.35
			if limit < 4 {
				limit = 4
			}
			if g.face == last.face && pitch >= -0.25*last.size && pitch <= limit {
				cur = append(cur, g)
				continue
			}
			flush()
			cur = []glyph{g}
		}
		flush()
	}
	attached := make([][]glyph, len(runs))
	var looseNotes []glyph
	for _, n := range notes {
		best := -1
		bestScore := math.MaxFloat64
		for i, r := range runs {
			dy := n.y - r.y
			if dy < -r.size*0.25 || dy > r.size*0.7 {
				continue
			}
			minX := r.glyphs[0].x
			last := r.glyphs[len(r.glyphs)-1]
			maxX := last.x + math.Max(last.w, last.size*0.8)
			// 引用经常落在两个词之间的空隙里, 空隙比一个字宽要大, 仍算前一个词的上标.
			if n.x < minX-r.size*2.2 || n.x > maxX+r.size*2.6 {
				continue
			}
			score := math.Abs(dy)
			if n.x > maxX {
				score += (n.x - maxX) * 0.02
			}
			if n.x < minX {
				score += (minX - n.x) * 0.02
			}
			if score < bestScore {
				bestScore = score
				best = i
			}
		}
		if best < 0 {
			// 对不上词时也贴到最近的正文基线, 避免上标的 y 把它排进上一行末尾.
			n.line = n.y
			bestDy := math.MaxFloat64
			for _, r := range runs {
				dy := n.y - r.y
				if dy < -r.size*0.25 || dy > r.size*0.8 || dy >= bestDy {
					continue
				}
				bestDy = dy
				n.line = r.y
			}
			looseNotes = append(looseNotes, n)
			continue
		}
		attached[best] = append(attached[best], n)
	}
	var out []glyph
	for i, r := range runs {
		ns := attached[i]
		sort.SliceStable(ns, func(a, b int) bool { return ns[a].x < ns[b].x })
		out = append(out, spliceRun(r.glyphs, ns, r.y)...)
	}
	out = append(out, looseNotes...)
	return out
}

func clusterByY(gs []glyph, factor float64) [][]glyph {
	var lines [][]glyph
	for _, g := range gs {
		if len(lines) == 0 {
			lines = append(lines, []glyph{g})
			continue
		}
		line := lines[len(lines)-1]
		base := line[0]
		if math.Abs(base.y-g.y) <= math.Max(base.size, g.size)*factor {
			lines[len(lines)-1] = append(line, g)
			continue
		}
		lines = append(lines, []glyph{g})
	}
	return lines
}

func upperQuartileSize(line []glyph) float64 {
	if len(line) == 0 {
		return 0
	}
	ss := make([]float64, len(line))
	for i, g := range line {
		ss[i] = g.size
	}
	sort.Float64s(ss)
	return ss[len(ss)*3/4]
}

// spliceRun 把上标插到它覆盖的那个字后面, 正文仍按从左到右连成词.
func spliceRun(body, notes []glyph, baseY float64) []glyph {
	var out []glyph
	ni := 0
	var cur glyph
	has := false
	flush := func() {
		if !has {
			return
		}
		out = append(out, cur)
		has = false
	}
	for _, g := range body {
		g.line = baseY
		for ni < len(notes) && notes[ni].x+notes[ni].size*0.25 < g.x {
			flush()
			n := notes[ni]
			n.line = baseY
			out = appendNote(out, n)
			ni++
		}
		if !has {
			cur = g
			has = true
			continue
		}
		cur.s += g.s
		if g.x+g.w > cur.x+cur.w {
			cur.w = g.x + g.w - cur.x
		}
	}
	flush()
	for ; ni < len(notes); ni++ {
		n := notes[ni]
		n.line = baseY
		out = appendNote(out, n)
	}
	return out
}

func appendNote(out []glyph, n glyph) []glyph {
	if len(out) == 0 {
		return append(out, n)
	}
	prev := &out[len(out)-1]
	pitch := n.x - prev.x
	if prev.face == n.face && math.Abs(prev.size-n.size) < 0.5 && pitch >= -0.3*n.size && pitch <= math.Max(prev.size, n.size)*1.6 {
		prev.s += n.s
		if n.x+n.w > prev.x+prev.w {
			prev.w = n.x + n.w - prev.x
		}
		return out
	}
	return append(out, n)
}

func indexRawImages(pdfBytes []byte) []rawImage {
	lengths := map[int]int{}
	for _, m := range reIntObj.FindAllSubmatch(pdfBytes, -1) {
		id := atoi(m[1])
		lengths[id] = atoi(m[2])
	}
	var out []rawImage
	pos := 0
	for {
		i := bytes.Index(pdfBytes[pos:], []byte("/Subtype /Image"))
		if i < 0 {
			break
		}
		i += pos
		pos = i + len("/Subtype /Image")
		obj := objectStart(pdfBytes, i)
		rel := bytes.Index(pdfBytes[i:], []byte("stream"))
		if rel < 0 {
			continue
		}
		streamAt := i + rel
		window := pdfBytes[obj:streamAt]
		wm := reWidth.FindAllSubmatch(window, -1)
		hm := reHeight.FindAllSubmatch(window, -1)
		lm := reLength.FindAllSubmatch(window, -1)
		if len(wm) == 0 || len(hm) == 0 || len(lm) == 0 {
			continue
		}
		w := atoi(wm[len(wm)-1][1])
		h := atoi(hm[len(hm)-1][1])
		lastLen := lm[len(lm)-1]
		length := atoi(lastLen[1])
		if len(lastLen[2]) > 0 {
			length = lengths[length]
		}
		if w < 8 || h < 8 || length <= 0 || streamAt+6+length > len(pdfBytes) {
			continue
		}
		j := streamAt + len("stream")
		if j+1 < len(pdfBytes) && pdfBytes[j] == '\r' && pdfBytes[j+1] == '\n' {
			j += 2
		} else if j < len(pdfBytes) && (pdfBytes[j] == '\n' || pdfBytes[j] == '\r') {
			j++
		}
		if j+length > len(pdfBytes) {
			continue
		}
		tail := bytes.TrimLeft(pdfBytes[j+length:], "\r\n")
		if !bytes.HasPrefix(tail, []byte("endstream")) {
			continue
		}
		out = append(out, rawImage{w: w, h: h, length: length, data: pdfBytes[j : j+length]})
	}
	return out
}

func objectStart(data []byte, before int) int {
	region := data[:before]
	j := bytes.LastIndex(region, []byte(" obj"))
	if j < 0 {
		return 0
	}
	return j
}

func atoi(b []byte) int {
	n := 0
	for _, c := range b {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	return n
}

func takeRaw(images []rawImage, w, h, length int) []byte {
	for i := range images {
		if !images[i].used && images[i].w == w && images[i].h == h && images[i].length == length {
			images[i].used = true
			return images[i].data
		}
	}
	for i := range images {
		if images[i].w == w && images[i].h == h && images[i].length == length {
			return images[i].data
		}
	}
	return nil
}

func imageFile(obj pdf.Value, raws []rawImage) (ext string, data []byte) {
	defer func() { recover() }()
	w := int(obj.Key("Width").Int64())
	h := int(obj.Key("Height").Int64())
	length := int(obj.Key("Length").Int64())
	if w <= 0 || h <= 0 || w*h > 25_000_000 {
		return "", nil
	}
	raw := takeRaw(raws, w, h, length)
	if len(raw) == 0 {
		return "", nil
	}
	switch filterOf(obj) {
	case "DCTDecode":
		if len(raw) < 2 || raw[0] != 0xff || raw[1] != 0xd8 {
			return "", nil
		}
		return "jpg", raw
	case "JPXDecode":
		if img, err := decodeJP2(raw); err == nil && img != nil {
			if pngBytes, pngErr := encodePNG(img); pngErr == nil {
				return "png", pngBytes
			}
		}
		return "jp2", raw
	case "FlateDecode":
		pngBytes, err := flateToPNG(raw, obj)
		if err != nil {
			return "", nil
		}
		return "png", pngBytes
	default:
		return "", nil
	}
}

func filterOf(v pdf.Value) string {
	f := v.Key("Filter")
	switch f.Kind() {
	case pdf.Name:
		return f.Name()
	case pdf.Array:
		if f.Len() == 0 {
			return ""
		}
		return f.Index(f.Len() - 1).Name()
	default:
		return ""
	}
}

func decodeJP2(raw []byte) (img image.Image, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("jpeg2000: %v", r)
		}
	}()
	return jp2k.Decode(bytes.NewReader(raw))
}

func encodePNG(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func flateToPNG(raw []byte, v pdf.Value) ([]byte, error) {
	pix, err := inflate(raw)
	if err != nil {
		return nil, err
	}
	w := int(v.Key("Width").Int64())
	h := int(v.Key("Height").Int64())
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("bad size")
	}
	bits := int(v.Key("BitsPerComponent").Int64())
	if bits == 0 {
		bits = 8
	}
	if bits != 8 {
		return nil, fmt.Errorf("bits %d", bits)
	}
	ncomp := componentCount(v)
	dp := decodeParms(v)
	pred := int(dp.Key("Predictor").Int64())
	if ncomp == 0 {
		switch {
		case len(pix) == w*h:
			ncomp = 1
		case len(pix) == w*h*3:
			ncomp = 3
		case len(pix) == w*h*4:
			ncomp = 4
		case len(pix) == (w+1)*h:
			ncomp = 1
			pred = 15
		case len(pix) == (w*3+1)*h:
			ncomp = 3
			pred = 15
		default:
			return nil, fmt.Errorf("pixel size %d", len(pix))
		}
	}
	if pred >= 10 {
		pix, err = pngUnfilter(pix, w, h, ncomp)
		if err != nil {
			return nil, err
		}
	}
	if len(pix) < w*h*ncomp {
		return nil, fmt.Errorf("short pixels")
	}
	cs := v.Key("ColorSpace").Name()
	var im image.Image
	switch ncomp {
	case 1:
		g := image.NewGray(image.Rect(0, 0, w, h))
		for y := 0; y < h; y++ {
			copy(g.Pix[y*g.Stride:y*g.Stride+w], pix[y*w:(y+1)*w])
		}
		im = g
	case 3:
		rgba := image.NewRGBA(image.Rect(0, 0, w, h))
		for y := 0; y < h; y++ {
			row := pix[y*w*3 : (y+1)*w*3]
			for x := 0; x < w; x++ {
				off := y*rgba.Stride + x*4
				rgba.Pix[off] = row[x*3]
				rgba.Pix[off+1] = row[x*3+1]
				rgba.Pix[off+2] = row[x*3+2]
				rgba.Pix[off+3] = 255
			}
		}
		im = rgba
	case 4:
		if strings.Contains(cs, "CMYK") {
			return nil, fmt.Errorf("cmyk")
		}
		rgba := image.NewRGBA(image.Rect(0, 0, w, h))
		for y := 0; y < h; y++ {
			row := pix[y*w*4 : (y+1)*w*4]
			for x := 0; x < w; x++ {
				off := y*rgba.Stride + x*4
				copy(rgba.Pix[off:off+4], row[x*4:x*4+4])
			}
		}
		im = rgba
	default:
		return nil, fmt.Errorf("components %d", ncomp)
	}
	return encodePNG(im)
}

func inflate(raw []byte) ([]byte, error) {
	zr, err := zlib.NewReader(bytes.NewReader(raw))
	if err == nil {
		defer zr.Close()
		return io.ReadAll(zr)
	}
	fr := flate.NewReader(bytes.NewReader(raw))
	defer fr.Close()
	return io.ReadAll(fr)
}

func decodeParms(v pdf.Value) pdf.Value {
	dp := v.Key("DecodeParms")
	if dp.Kind() == pdf.Array && dp.Len() > 0 {
		return dp.Index(0)
	}
	return dp
}

func componentCount(v pdf.Value) int {
	switch v.Key("ColorSpace").Name() {
	case "DeviceGray", "CalGray":
		return 1
	case "DeviceRGB", "CalRGB":
		return 3
	}
	dp := decodeParms(v)
	if c := int(dp.Key("Colors").Int64()); c == 1 || c == 3 || c == 4 {
		return c
	}
	return 0
}

func pngUnfilter(data []byte, w, h, ncomp int) ([]byte, error) {
	rowSize := w * ncomp
	stride := rowSize + 1
	if len(data) < stride*h {
		return nil, fmt.Errorf("predictor short")
	}
	out := make([]byte, rowSize*h)
	prev := make([]byte, rowSize)
	for y := 0; y < h; y++ {
		row := data[y*stride : (y+1)*stride]
		cur := out[y*rowSize : (y+1)*rowSize]
		in := row[1:]
		switch row[0] {
		case 0:
			copy(cur, in)
		case 1:
			for i := 0; i < rowSize; i++ {
				var left byte
				if i >= ncomp {
					left = cur[i-ncomp]
				}
				cur[i] = in[i] + left
			}
		case 2:
			for i := 0; i < rowSize; i++ {
				cur[i] = in[i] + prev[i]
			}
		case 3:
			for i := 0; i < rowSize; i++ {
				var left byte
				if i >= ncomp {
					left = cur[i-ncomp]
				}
				cur[i] = in[i] + byte((int(left)+int(prev[i]))/2)
			}
		case 4:
			for i := 0; i < rowSize; i++ {
				var a, c byte
				if i >= ncomp {
					a = cur[i-ncomp]
					c = prev[i-ncomp]
				}
				cur[i] = in[i] + paeth(a, prev[i], c)
			}
		default:
			return nil, fmt.Errorf("filter %d", row[0])
		}
		copy(prev, cur)
	}
	return out, nil
}

func paeth(a, b, c byte) byte {
	p := int(a) + int(b) - int(c)
	pa := absInt(p - int(a))
	pb := absInt(p - int(b))
	pc := absInt(p - int(c))
	if pa <= pb && pa <= pc {
		return a
	}
	if pb <= pc {
		return b
	}
	return c
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// 有些知网 CID 字体没有 cmap, ToUnicode 又和真正画出来的轮廓对不上.
// 把用到的轮廓嵌进页面后, 浏览器按轮廓画, 不再拿系统字体去套那个错误码位.
type embeddedFace struct {
	family string
	raw    []byte
	glyphs map[int]rune
}

type fontSlot struct {
	face  *embeddedFace
	gidOf []int
}

type fontBook struct {
	byPtr map[*pdf.Font]*fontSlot
	bySum map[string]*embeddedFace
	faces []*embeddedFace
}

func (b *fontBook) use(f *pdf.Font, cid int, ch rune) string {
	if b == nil || f == nil || ch <= 0 || ch >= 0xFFFF {
		return ""
	}
	slot, ok := b.byPtr[f]
	if !ok {
		slot = b.bind(f)
		b.byPtr[f] = slot
	}
	if slot == nil || slot.face == nil {
		return ""
	}
	gid := cid
	if slot.gidOf != nil {
		if cid < 0 || cid >= len(slot.gidOf) {
			return ""
		}
		gid = slot.gidOf[cid]
	}
	if gid <= 0 {
		return ""
	}
	if _, exists := slot.face.glyphs[gid]; !exists {
		slot.face.glyphs[gid] = ch
	}
	return slot.face.family
}

func (b *fontBook) bind(f *pdf.Font) *fontSlot {
	raw, gidOf := cidFontFile(f)
	if len(raw) == 0 {
		return &fontSlot{}
	}
	sum := sha256.Sum256(raw)
	face := b.bySum[string(sum[:])]
	if face == nil {
		face = &embeddedFace{
			family: fmt.Sprintf("cnki%d", len(b.faces)),
			raw:    raw,
			glyphs: map[int]rune{},
		}
		b.bySum[string(sum[:])] = face
		b.faces = append(b.faces, face)
	}
	return &fontSlot{face: face, gidOf: gidOf}
}

func (b *fontBook) css() string {
	if b == nil {
		return ""
	}
	var s strings.Builder
	for _, face := range b.faces {
		ttf, err := subsetTTF(face.raw, face.glyphs)
		if err != nil || len(ttf) == 0 {
			continue
		}
		fmt.Fprintf(&s, "@font-face{font-family:%s;src:url(data:font/ttf;base64,%s) format(\"truetype\")}\n", face.family, base64.StdEncoding.EncodeToString(ttf))
	}
	return s.String()
}

func cidFontFile(f *pdf.Font) (raw []byte, gidOf []int) {
	defer func() { recover() }()
	if f == nil {
		return nil, nil
	}
	enc := f.V.Key("Encoding").Name()
	if enc != "Identity-H" && enc != "Identity-V" {
		return nil, nil
	}
	kids := f.V.Key("DescendantFonts")
	if kids.Kind() != pdf.Array || kids.Len() == 0 {
		return nil, nil
	}
	cid := kids.Index(0)
	ff := cid.Key("FontDescriptor").Key("FontFile2")
	if ff.Kind() != pdf.Stream {
		return nil, nil
	}
	rc := ff.Reader()
	if rc == nil {
		return nil, nil
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil || len(data) < 12 || len(data) > 20<<20 {
		return nil, nil
	}
	scaler := binary.BigEndian.Uint32(data[:4])
	if scaler != 0x00010000 && scaler != 0x74727565 {
		return nil, nil
	}
	return data, readGIDMap(cid)
}

func readGIDMap(cid pdf.Value) (gidOf []int) {
	defer func() { recover() }()
	m := cid.Key("CIDToGIDMap")
	if m.Kind() != pdf.Stream {
		return nil
	}
	rc := m.Reader()
	if rc == nil {
		return nil
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil || len(b) < 2 {
		return nil
	}
	gidOf = make([]int, len(b)/2)
	for i := range gidOf {
		gidOf[i] = int(b[i*2])<<8 | int(b[i*2+1])
	}
	return gidOf
}
