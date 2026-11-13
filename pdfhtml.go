package main

import (
	"bytes"
	"compress/flate"
	"compress/zlib"
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

	"github.com/ledongthuc/pdf"
	"github.com/robert-mcdermott/jp2k"
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
	r, err := pdf.NewReader(bytes.NewReader(pdfBytes), int64(len(pdfBytes)))
	if err != nil {
		return out, err
	}
	rawImages := indexRawImages(pdfBytes)
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
			walkStream(contents, page.Resources(), matIdent(), 0, &glyphs, &images)
		}
		boxes := make([]htmlBox, 0, len(glyphs)+len(images))
		for _, run := range groupGlyphs(glyphs) {
			text := strings.TrimSpace(run.s)
			if text == "" {
				continue
			}
			out.TextChars += len([]rune(text))
			size := run.size
			if size < 1 {
				size = 10
			}
			left := (run.x - ox) * htmlScale
			top := (oy + ph - run.y - size*0.8) * htmlScale
			boxes = append(boxes, htmlBox{top, fmt.Sprintf(
				`<span style="left:%.1fpx;top:%.1fpx;font-size:%.1fpx">%s</span>`,
				left, top, size*htmlScale, html.EscapeString(run.s),
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
			boxes = append(boxes, htmlBox{top, fmt.Sprintf(
				`<img alt="%s" src="assets/%s" style="left:%.1fpx;top:%.1fpx;width:%.1fpx;height:%.1fpx">`,
				html.EscapeString(alt), name, left, top, width, height,
			)})
		}
		sort.SliceStable(boxes, func(a, b int) bool { return boxes[a].top < boxes[b].top })
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
	doc.WriteString("body{margin:0;background:#f3f3f3;color:#111;font-family:\"Source Han Serif SC\",\"Noto Serif CJK SC\",\"SimSun\",serif}\n")
	doc.WriteString("h1{font-size:22px;font-weight:600;margin:24px auto 8px;max-width:920px;padding:0 16px}\n")
	doc.WriteString(".page{position:relative;margin:16px auto;background:#fff;box-shadow:0 1px 4px rgba(0,0,0,.15);overflow:hidden}\n")
	doc.WriteString(".page span{position:absolute;white-space:pre;line-height:1;color:#111}\n")
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
	top  float64
	html string
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

type textState struct {
	tc, th, tl, tfs, trise float64
	tm, tlm, ctm           mat3
	font                   *pdf.Font
	enc                    pdf.TextEncoding
}

func walkStream(strm, resources pdf.Value, ctm mat3, depth int, glyphs *[]glyph, images *[]placedImg) {
	if depth > 6 || strm.IsNull() {
		return
	}
	defer func() { recover() }()
	g := textState{th: 1, ctm: ctm}
	var stack []textState
	fonts := map[string]*pdf.Font{}
	show := func(raw string) {
		if g.enc == nil || g.font == nil || raw == "" {
			return
		}
		decoded := g.enc.Decode(raw)
		n := 0
		for _, ch := range decoded {
			var w0 float64
			if n < len(raw) {
				w0 = g.font.Width(int(raw[n]))
			}
			n++
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
			if ch >= 32 {
				*glyphs = append(*glyphs, glyph{x: x, y: y, size: size, w: adv, s: string(ch)})
			}
			tx := w0/1000*g.tfs + g.tc
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
				walkStream(obj, res, fm.mul(g.ctm), depth+1, glyphs, images)
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
			g.tc = args[1].Float64()
			x := matFrom(1, 0, 0, 1, 0, -g.tl)
			g.tlm = x.mul(g.tlm)
			g.tm = g.tlm
			show(args[2].RawString())
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
	sort.SliceStable(gs, func(i, j int) bool { return gs[i].y > gs[j].y })
	var lines [][]glyph
	for _, g := range gs {
		if len(lines) == 0 {
			lines = append(lines, []glyph{g})
			continue
		}
		base := lines[len(lines)-1][0]
		if math.Abs(base.y-g.y) <= math.Max(base.size, g.size)*0.45 {
			lines[len(lines)-1] = append(lines[len(lines)-1], g)
			continue
		}
		lines = append(lines, []glyph{g})
	}
	var runs []glyph
	for _, line := range lines {
		sort.SliceStable(line, func(i, j int) bool { return line[i].x < line[j].x })
		cur := line[0]
		for _, g := range line[1:] {
			limit := math.Max(cur.size, g.size) * 1.25
			if limit < 4 {
				limit = 4
			}
			if g.x >= cur.x-1 && g.x-cur.x < limit {
				cur.s += g.s
				if g.x+g.w > cur.x+cur.w {
					cur.w = g.x + g.w - cur.x
				}
				continue
			}
			runs = append(runs, cur)
			cur = g
		}
		runs = append(runs, cur)
	}
	return runs
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
