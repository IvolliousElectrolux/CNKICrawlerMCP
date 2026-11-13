package main

import (
	"encoding/binary"
	"errors"
	"sort"
)

// subsetTTF 从没有 cmap 的 CID 字体里抽出用到的字形, 并补一张
// Unicode 到字形编号的对照. 浏览器因此画出 PDF 里的轮廓.
// 这里不看轮廓像哪个字, 只沿用 PDF 已经写好的码位.
func subsetTTF(font []byte, gidToRune map[int]rune) ([]byte, error) {
	tables, err := readTTFTables(font)
	if err != nil {
		return nil, err
	}
	glyf := tables["glyf"]
	loca := tables["loca"]
	head := append([]byte(nil), tables["head"]...)
	hhea := append([]byte(nil), tables["hhea"]...)
	hmtx := tables["hmtx"]
	maxp := append([]byte(nil), tables["maxp"]...)
	if glyf == nil || loca == nil || len(head) < 54 || len(hhea) < 36 || len(maxp) < 32 || len(hmtx) < 4 {
		return nil, errors.New("字体缺表")
	}
	if binary.BigEndian.Uint32(maxp[:4]) != 0x00010000 {
		return nil, errors.New("不是 TrueType 字形")
	}
	numGlyphs := int(binary.BigEndian.Uint16(maxp[4:6]))
	if numGlyphs < 1 {
		return nil, errors.New("字体没有字形")
	}
	longLoca := binary.BigEndian.Uint16(head[50:52]) == 1
	nMetrics := int(binary.BigEndian.Uint16(hhea[34:36]))
	if nMetrics < 1 {
		nMetrics = 1
	}

	used := map[int]bool{0: true}
	for gid := range gidToRune {
		if gid > 0 && gid < numGlyphs {
			used[gid] = true
		}
	}
	if len(used) < 2 {
		return nil, errors.New("没有可嵌入的字形")
	}
	pending := true
	for pending {
		pending = false
		for gid := range used {
			g, ok := glyphBytes(glyf, loca, longLoca, gid)
			if !ok {
				continue
			}
			for _, comp := range compositeGlyphs(g) {
				if comp > 0 && comp < numGlyphs && !used[comp] {
					used[comp] = true
					pending = true
				}
			}
		}
	}

	order := make([]int, 0, len(used))
	for gid := range used {
		if gid != 0 {
			order = append(order, gid)
		}
	}
	sort.Ints(order)
	order = append([]int{0}, order...)
	if len(order) > 65535 {
		return nil, errors.New("字形太多")
	}
	remap := make(map[int]int, len(order))
	for i, gid := range order {
		remap[gid] = i
	}

	var newGlyf []byte
	locaOff := make([]uint32, len(order)+1)
	newHmtx := make([]byte, 0, len(order)*4)
	var maxAdv uint16
	for i, gid := range order {
		locaOff[i] = uint32(len(newGlyf))
		g, _ := glyphBytes(glyf, loca, longLoca, gid)
		g = retargetComposite(g, remap)
		if len(g)%2 == 1 {
			g = append(g, 0)
		}
		newGlyf = append(newGlyf, g...)
		adv, lsb := hMetric(hmtx, nMetrics, gid)
		if adv > maxAdv {
			maxAdv = adv
		}
		var metric [4]byte
		binary.BigEndian.PutUint16(metric[0:2], adv)
		binary.BigEndian.PutUint16(metric[2:4], uint16(lsb))
		newHmtx = append(newHmtx, metric[:]...)
	}
	locaOff[len(order)] = uint32(len(newGlyf))
	newLoca := make([]byte, len(locaOff)*4)
	for i, off := range locaOff {
		binary.BigEndian.PutUint32(newLoca[i*4:], off)
	}

	binary.BigEndian.PutUint32(head[8:12], 0)
	binary.BigEndian.PutUint16(head[50:52], 1)
	binary.BigEndian.PutUint16(hhea[10:12], maxAdv)
	binary.BigEndian.PutUint16(hhea[34:36], uint16(len(order)))
	binary.BigEndian.PutUint16(maxp[4:6], uint16(len(order)))

	cmap := buildCmap(remap, gidToRune)
	if cmap == nil {
		return nil, errors.New("没有码位")
	}
	first, last := runeSpan(gidToRune)
	outTables := []ttfTable{
		{tag: "OS/2", data: os2Table(hhea, maxAdv, first, last)},
		{tag: "cmap", data: cmap},
		{tag: "glyf", data: newGlyf},
		{tag: "head", data: head},
		{tag: "hhea", data: hhea},
		{tag: "hmtx", data: newHmtx},
		{tag: "loca", data: newLoca},
		{tag: "maxp", data: maxp},
		{tag: "name", data: nameTable()},
		{tag: "post", data: postTable()},
	}
	for _, tag := range []string{"cvt ", "fpgm", "prep"} {
		if len(tables[tag]) > 0 {
			outTables = append(outTables, ttfTable{tag: tag, data: tables[tag]})
		}
	}
	return assembleTTF(outTables), nil
}

type ttfTable struct {
	tag  string
	data []byte
}

func readTTFTables(font []byte) (map[string][]byte, error) {
	if len(font) < 12 {
		return nil, errors.New("字体太短")
	}
	scaler := binary.BigEndian.Uint32(font[:4])
	if scaler != 0x00010000 && scaler != 0x74727565 {
		return nil, errors.New("不是 TrueType")
	}
	n := int(binary.BigEndian.Uint16(font[4:6]))
	if n < 1 || 12+n*16 > len(font) {
		return nil, errors.New("字体目录损坏")
	}
	out := make(map[string][]byte, n)
	for i := 0; i < n; i++ {
		rec := 12 + i*16
		tag := string(font[rec : rec+4])
		off := int(binary.BigEndian.Uint32(font[rec+8 : rec+12]))
		ln := int(binary.BigEndian.Uint32(font[rec+12 : rec+16]))
		if off < 0 || ln < 0 || off+ln > len(font) {
			return nil, errors.New("字体表越界")
		}
		out[tag] = font[off : off+ln]
	}
	return out, nil
}

func glyphBytes(glyf, loca []byte, long bool, gid int) ([]byte, bool) {
	a, ok1 := locaOffset(loca, long, gid)
	b, ok2 := locaOffset(loca, long, gid+1)
	if !ok1 || !ok2 || b < a || int(b) > len(glyf) {
		return nil, false
	}
	return glyf[a:b], true
}

func locaOffset(loca []byte, long bool, i int) (uint32, bool) {
	if long {
		o := i * 4
		if o < 0 || o+4 > len(loca) {
			return 0, false
		}
		return binary.BigEndian.Uint32(loca[o : o+4]), true
	}
	o := i * 2
	if o < 0 || o+2 > len(loca) {
		return 0, false
	}
	return uint32(binary.BigEndian.Uint16(loca[o:o+2])) * 2, true
}

// compositeGlyphs 读出组合字形引用的部件编号. 简单字形返回空.
func compositeGlyphs(g []byte) []int {
	if len(g) < 10 {
		return nil
	}
	if int16(binary.BigEndian.Uint16(g[:2])) >= 0 {
		return nil
	}
	var out []int
	i := 10
	for i+4 <= len(g) {
		flags := binary.BigEndian.Uint16(g[i : i+2])
		out = append(out, int(binary.BigEndian.Uint16(g[i+2:i+4])))
		i += 4
		if flags&0x0001 != 0 {
			i += 4
		} else {
			i += 2
		}
		switch {
		case flags&0x0008 != 0:
			i += 2
		case flags&0x0040 != 0:
			i += 4
		case flags&0x0080 != 0:
			i += 8
		}
		if flags&0x0020 == 0 {
			break
		}
	}
	return out
}

func retargetComposite(g []byte, remap map[int]int) []byte {
	if len(g) < 10 || int16(binary.BigEndian.Uint16(g[:2])) >= 0 {
		return append([]byte(nil), g...)
	}
	out := append([]byte(nil), g...)
	i := 10
	for i+4 <= len(out) {
		flags := binary.BigEndian.Uint16(out[i : i+2])
		old := int(binary.BigEndian.Uint16(out[i+2 : i+4]))
		if n, ok := remap[old]; ok {
			binary.BigEndian.PutUint16(out[i+2:i+4], uint16(n))
		}
		i += 4
		if flags&0x0001 != 0 {
			i += 4
		} else {
			i += 2
		}
		switch {
		case flags&0x0008 != 0:
			i += 2
		case flags&0x0040 != 0:
			i += 4
		case flags&0x0080 != 0:
			i += 8
		}
		if flags&0x0020 == 0 {
			break
		}
	}
	return out
}

func hMetric(hmtx []byte, nMetrics, gid int) (uint16, int16) {
	if nMetrics < 1 || len(hmtx) < 4 {
		return 1000, 0
	}
	if gid < 0 {
		gid = 0
	}
	if gid < nMetrics {
		o := gid * 4
		if o+4 > len(hmtx) {
			return 1000, 0
		}
		return binary.BigEndian.Uint16(hmtx[o : o+2]), int16(binary.BigEndian.Uint16(hmtx[o+2 : o+4]))
	}
	last := (nMetrics - 1) * 4
	if last+4 > len(hmtx) {
		return 1000, 0
	}
	adv := binary.BigEndian.Uint16(hmtx[last : last+2])
	o := nMetrics*4 + (gid-nMetrics)*2
	if o+2 > len(hmtx) {
		return adv, 0
	}
	return adv, int16(binary.BigEndian.Uint16(hmtx[o : o+2]))
}

type cmapPair struct {
	cp  uint16
	gid uint16
}

func buildCmap(remap map[int]int, gidToRune map[int]rune) []byte {
	byCP := map[uint16]uint16{}
	for gid, ch := range gidToRune {
		if ch <= 0 || ch >= 0xFFFF {
			continue
		}
		newGid, ok := remap[gid]
		if !ok || newGid <= 0 || newGid > 0xFFFF {
			continue
		}
		cp := uint16(ch)
		if _, exists := byCP[cp]; exists {
			continue
		}
		byCP[cp] = uint16(newGid)
	}
	if len(byCP) == 0 {
		return nil
	}
	pairs := make([]cmapPair, 0, len(byCP))
	for cp, gid := range byCP {
		pairs = append(pairs, cmapPair{cp: cp, gid: gid})
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].cp < pairs[j].cp })

	segs := len(pairs) + 1
	f4len := 16 + segs*8
	buf := make([]byte, 20+f4len)
	binary.BigEndian.PutUint16(buf[2:], 2)
	binary.BigEndian.PutUint16(buf[6:], 3)
	binary.BigEndian.PutUint32(buf[8:], 20)
	binary.BigEndian.PutUint16(buf[12:], 3)
	binary.BigEndian.PutUint16(buf[14:], 1)
	binary.BigEndian.PutUint32(buf[16:], 20)
	writeFormat4(buf[20:], pairs)
	return buf
}

func writeFormat4(b []byte, pairs []cmapPair) {
	segs := len(pairs) + 1
	pow, es := 1, 0
	for pow*2 <= segs {
		pow *= 2
		es++
	}
	searchRange := pow * 2
	rangeShift := segs*2 - searchRange
	binary.BigEndian.PutUint16(b[0:], 4)
	binary.BigEndian.PutUint16(b[2:], uint16(len(b)))
	binary.BigEndian.PutUint16(b[6:], uint16(segs*2))
	binary.BigEndian.PutUint16(b[8:], uint16(searchRange))
	binary.BigEndian.PutUint16(b[10:], uint16(es))
	binary.BigEndian.PutUint16(b[12:], uint16(rangeShift))
	endAt := 14
	startAt := 16 + segs*2
	deltaAt := 16 + segs*4
	offAt := 16 + segs*6
	for i, p := range pairs {
		binary.BigEndian.PutUint16(b[endAt+i*2:], p.cp)
		binary.BigEndian.PutUint16(b[startAt+i*2:], p.cp)
		binary.BigEndian.PutUint16(b[deltaAt+i*2:], p.gid-p.cp)
		binary.BigEndian.PutUint16(b[offAt+i*2:], 0)
	}
	last := segs - 1
	binary.BigEndian.PutUint16(b[endAt+last*2:], 0xFFFF)
	binary.BigEndian.PutUint16(b[startAt+last*2:], 0xFFFF)
	binary.BigEndian.PutUint16(b[deltaAt+last*2:], 1)
}

func nameTable() []byte {
	type item struct {
		id uint16
		s  string
	}
	items := []item{{1, "CNKI"}, {2, "Regular"}, {4, "CNKI"}, {6, "CNKI"}}
	encoded := make([][]byte, len(items))
	stringsLen := 0
	for i, it := range items {
		encoded[i] = utf16BE(it.s)
		stringsLen += len(encoded[i])
	}
	stringOff := 6 + 12*len(items)
	buf := make([]byte, stringOff+stringsLen)
	binary.BigEndian.PutUint16(buf[2:], uint16(len(items)))
	binary.BigEndian.PutUint16(buf[4:], uint16(stringOff))
	pos := 0
	for i, it := range items {
		rec := 6 + i*12
		binary.BigEndian.PutUint16(buf[rec:], 3)
		binary.BigEndian.PutUint16(buf[rec+2:], 1)
		binary.BigEndian.PutUint16(buf[rec+4:], 0x0409)
		binary.BigEndian.PutUint16(buf[rec+6:], it.id)
		binary.BigEndian.PutUint16(buf[rec+8:], uint16(len(encoded[i])))
		binary.BigEndian.PutUint16(buf[rec+10:], uint16(pos))
		copy(buf[stringOff+pos:], encoded[i])
		pos += len(encoded[i])
	}
	return buf
}

func postTable() []byte {
	b := make([]byte, 32)
	binary.BigEndian.PutUint32(b[0:], 0x00030000)
	return b
}

func runeSpan(gidToRune map[int]rune) (uint16, uint16) {
	first, last := uint16(0xFFFF), uint16(0)
	for _, ch := range gidToRune {
		if ch <= 0 || ch >= 0xFFFF {
			continue
		}
		cp := uint16(ch)
		if cp < first {
			first = cp
		}
		if cp > last {
			last = cp
		}
	}
	if first > last {
		return 0, 0
	}
	return first, last
}

func os2Table(hhea []byte, avgWidth, first, last uint16) []byte {
	if avgWidth == 0 {
		avgWidth = 256
	}
	b := make([]byte, 96)
	binary.BigEndian.PutUint16(b[0:], 4)
	binary.BigEndian.PutUint16(b[2:], avgWidth)
	binary.BigEndian.PutUint16(b[4:], 400)
	binary.BigEndian.PutUint16(b[6:], 5)
	copy(b[58:62], []byte("CNKI"))
	binary.BigEndian.PutUint16(b[62:], 0x00C0)
	binary.BigEndian.PutUint16(b[64:], first)
	binary.BigEndian.PutUint16(b[66:], last)
	asc := binary.BigEndian.Uint16(hhea[4:6])
	desc := int16(binary.BigEndian.Uint16(hhea[6:8]))
	gap := binary.BigEndian.Uint16(hhea[8:10])
	binary.BigEndian.PutUint16(b[68:], asc)
	binary.BigEndian.PutUint16(b[70:], uint16(desc))
	binary.BigEndian.PutUint16(b[72:], gap)
	binary.BigEndian.PutUint16(b[74:], asc)
	winDesc := desc
	if winDesc < 0 {
		winDesc = -winDesc
	}
	binary.BigEndian.PutUint16(b[76:], uint16(winDesc))
	binary.BigEndian.PutUint32(b[46:], 1<<27)
	binary.BigEndian.PutUint32(b[78:], 1<<18)
	binary.BigEndian.PutUint16(b[86:], asc/2)
	binary.BigEndian.PutUint16(b[88:], asc)
	binary.BigEndian.PutUint16(b[92:], 0x0020)
	binary.BigEndian.PutUint16(b[94:], 1)
	return b
}

func utf16BE(s string) []byte {
	u := []rune(s)
	out := make([]byte, len(u)*2)
	for i, r := range u {
		binary.BigEndian.PutUint16(out[i*2:], uint16(r))
	}
	return out
}

func assembleTTF(tables []ttfTable) []byte {
	sort.Slice(tables, func(i, j int) bool { return tables[i].tag < tables[j].tag })
	n := len(tables)
	pow, es := 1, 0
	for pow*2 <= n {
		pow *= 2
		es++
	}
	header := make([]byte, 12+n*16)
	binary.BigEndian.PutUint32(header[0:], 0x00010000)
	binary.BigEndian.PutUint16(header[4:], uint16(n))
	binary.BigEndian.PutUint16(header[6:], uint16(pow*16))
	binary.BigEndian.PutUint16(header[8:], uint16(es))
	binary.BigEndian.PutUint16(header[10:], uint16(n*16-pow*16))

	body := make([]byte, 0, 1<<16)
	off := len(header)
	headAt := -1
	for i, t := range tables {
		padded := t.data
		if len(padded)%4 != 0 {
			tmp := make([]byte, len(padded)+(4-len(padded)%4))
			copy(tmp, padded)
			padded = tmp
		}
		rec := 12 + i*16
		copy(header[rec:rec+4], t.tag)
		binary.BigEndian.PutUint32(header[rec+4:], checksum32(padded))
		binary.BigEndian.PutUint32(header[rec+8:], uint32(off))
		binary.BigEndian.PutUint32(header[rec+12:], uint32(len(t.data)))
		if t.tag == "head" {
			headAt = off
		}
		body = append(body, padded...)
		off += len(padded)
	}
	file := append(header, body...)
	if headAt >= 0 && headAt+12 <= len(file) {
		adj := 0xB1B0AFBA - checksum32(file)
		binary.BigEndian.PutUint32(file[headAt+8:], adj)
	}
	return file
}

func checksum32(b []byte) uint32 {
	var sum uint32
	n := len(b) - len(b)%4
	for i := 0; i < n; i += 4 {
		sum += binary.BigEndian.Uint32(b[i : i+4])
	}
	if n < len(b) {
		var tail [4]byte
		copy(tail[:], b[n:])
		sum += binary.BigEndian.Uint32(tail[:])
	}
	return sum
}
