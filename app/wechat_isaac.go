package main

import (
	"encoding/binary"
	"errors"
	"strconv"
	"strings"
)

// ISAAC-64 follows Bob Jenkins's public-domain reference (1996).
// The byte order and seed convention were matched against the original
// res-downloader's WxIsaac64 WASM for all 131072 output bytes, not inferred
// from the Base64 string accepted by its native file writer.
type wechatIsaac struct {
	mem, results [256]uint64
	a, b, c      uint64
	remaining    int
}

func wechatMix(v [8]uint64) [8]uint64 {
	a, b, c, d, e, f, g, h := v[0], v[1], v[2], v[3], v[4], v[5], v[6], v[7]
	a -= e
	f ^= h >> 9
	h += a
	b -= f
	g ^= a << 9
	a += b
	c -= g
	h ^= b >> 23
	b += c
	d -= h
	a ^= c << 15
	c += d
	e -= a
	b ^= d >> 14
	d += e
	f -= b
	c ^= e << 20
	e += f
	g -= c
	d ^= f >> 17
	f += g
	h -= d
	e ^= g << 14
	g += h
	return [8]uint64{a, b, c, d, e, f, g, h}
}

func newWechatIsaac(seed uint64) *wechatIsaac {
	s := &wechatIsaac{}
	s.results[0] = seed
	var v [8]uint64
	for i := range v {
		v[i] = 0x9e3779b97f4a7c13
	}
	for i := 0; i < 4; i++ {
		v = wechatMix(v)
	}
	for i := 0; i < 256; i += 8 {
		for j := range v {
			v[j] += s.results[i+j]
		}
		v = wechatMix(v)
		copy(s.mem[i:i+8], v[:])
	}
	for i := 0; i < 256; i += 8 {
		for j := range v {
			v[j] += s.mem[i+j]
		}
		v = wechatMix(v)
		copy(s.mem[i:i+8], v[:])
	}
	s.fill()
	return s
}

func (s *wechatIsaac) fill() {
	s.c++
	s.b += s.c
	for i := 0; i < 256; i++ {
		x := s.mem[i]
		switch i & 3 {
		case 0:
			s.a = ^(s.a ^ (s.a << 21))
		case 1:
			s.a ^= s.a >> 5
		case 2:
			s.a ^= s.a << 12
		case 3:
			s.a ^= s.a >> 33
		}
		s.a += s.mem[(i+128)&255]
		y := s.mem[(x>>3)&255] + s.a + s.b
		s.mem[i] = y
		s.b = s.mem[(y>>11)&255] + x
		s.results[i] = s.b
	}
	s.remaining = 256
}

func wechatDecodeMask(rawSeed string) ([]byte, error) {
	seedText := strings.TrimSpace(rawSeed)
	if seedText == "" || len(seedText) > 20 {
		return nil, errors.New("视频号解码信息缺失，请开启捕获并在微信中重新播放视频")
	}
	for _, c := range seedText {
		if c < '0' || c > '9' {
			return nil, errors.New("视频号解码信息格式不受支持，请重新捕获当前视频")
		}
	}
	seed, err := strconv.ParseUint(seedText, 10, 64)
	if err != nil {
		return nil, errors.New("视频号解码信息超出有效范围")
	}
	s := newWechatIsaac(seed)
	mask := make([]byte, 131072)
	for i := 0; i < len(mask); i += 8 {
		if s.remaining == 0 {
			s.fill()
		}
		s.remaining--
		binary.BigEndian.PutUint64(mask[i:i+8], s.results[s.remaining])
	}
	return mask, nil
}
