package lab

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"
)

// Screen is what a VM's display shows: its size and its pixels, three bytes of RGB each, row by
// row from the top left.
type Screen struct {
	Width, Height int
	Pix           []byte
}

func (s *Screen) at(x, y int) [3]byte {
	i := 3 * (y*s.Width + x)
	return [3]byte(s.Pix[i : i+3])
}

// ReadPPM reads a binary PPM of 8-bit samples, as QEMU's screendump writes it.
func ReadPPM(r io.Reader) (*Screen, error) {
	br := bufio.NewReader(r)
	var magic string
	var w, h, maxval int
	if _, err := fmt.Fscan(br, &magic, &w, &h, &maxval); err != nil {
		return nil, fmt.Errorf("read PPM header: %w", err)
	}
	if magic != "P6" || maxval != 255 || w <= 0 || h <= 0 {
		return nil, fmt.Errorf("not a PPM of 8-bit RGB: %s %dx%d max %d", magic, w, h, maxval)
	}
	// One whitespace character ends the header.
	if _, err := br.ReadByte(); err != nil {
		return nil, err
	}
	s := &Screen{Width: w, Height: h, Pix: make([]byte, 3*w*h)}
	if _, err := io.ReadFull(br, s.Pix); err != nil {
		return nil, fmt.Errorf("read PPM pixels: %w", err)
	}
	return s, nil
}

// Font is a console font of the PSF2 format, as kbd's fonts are: glyphs of Width by Height
// pixels, and the character each draws.
type Font struct {
	Width, Height int
	// chars maps a glyph's bitmap, rows of whole bytes, the leftmost pixel in the high bit, to
	// the character it draws; an ASCII character where the font has one for the bitmap.
	chars map[string]rune
}

// ReadPSF reads a PSF2 font with a Unicode table, gzipped or plain.
func ReadPSF(path string) (*Font, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if bytes.HasPrefix(data, []byte{0x1f, 0x8b}) {
		zr, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if data, err = io.ReadAll(zr); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	}
	f, err := parsePSF(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return f, nil
}

func parsePSF(data []byte) (*Font, error) {
	if len(data) < 32 || binary.LittleEndian.Uint32(data) != 0x864ab572 {
		return nil, errors.New("not a PSF2 font")
	}
	field := func(i int) int { return int(binary.LittleEndian.Uint32(data[4*i:])) }
	headerSize, flags, length, charSize, height, width := field(2), field(3), field(4), field(5), field(6), field(7)
	end := headerSize + length*charSize
	if flags&1 == 0 || width == 0 || charSize != height*((width+7)/8) || end > len(data) {
		return nil, errors.New("a PSF2 font without a Unicode table, or of another layout")
	}
	f := &Font{Width: width, Height: height, chars: map[string]rune{}}
	table := data[end:]
	for g := range length {
		bitmap := string(data[headerSize+g*charSize : headerSize+(g+1)*charSize])
		i := bytes.IndexByte(table, 0xff)
		if i < 0 {
			return nil, errors.New("the font's Unicode table ends early")
		}
		entry := table[:i]
		table = table[i+1:]
		// Sequences of several characters follow 0xfe; single characters come before.
		if j := bytes.IndexByte(entry, 0xfe); j >= 0 {
			entry = entry[:j]
		}
		var char rune = -1
		for len(entry) > 0 {
			r, n := utf8.DecodeRune(entry)
			entry = entry[n:]
			if r != utf8.RuneError && (char < 0 || (char >= 0x80 && r < 0x80)) {
				char = r
			}
		}
		if old, ok := f.chars[bitmap]; char >= 0 && (!ok || (old >= 0x80 && char < 0x80)) {
			f.chars[bitmap] = char
		}
	}
	return f, nil
}

// Text reads the screen as the kernel's console draws it with the font: rows of cells of the
// font's size from the top left. A cell's most frequent colour is its background and its other
// pixels draw the glyph, so colours and reverse video read alike. A cell whose pixels no glyph
// draws, such as one under the cursor, reads as '?'. Each row's trailing spaces are cut.
func (f *Font) Text(s *Screen) []string {
	rowBytes := (f.Width + 7) / 8
	var rows []string
	for top := 0; top+f.Height <= s.Height; top += f.Height {
		var b strings.Builder
		for left := 0; left+f.Width <= s.Width; left += f.Width {
			bg := background(s, left, top, f.Width, f.Height)
			bitmap := make([]byte, rowBytes*f.Height)
			for y := range f.Height {
				for x := range f.Width {
					if s.at(left+x, top+y) != bg {
						bitmap[y*rowBytes+x/8] |= 0x80 >> (x % 8)
					}
				}
			}
			r, ok := f.chars[string(bitmap)]
			if !ok {
				r = '?'
			}
			b.WriteRune(r)
		}
		rows = append(rows, strings.TrimRight(b.String(), " "))
	}
	return rows
}

// background is the most frequent colour of the cell at left, top.
func background(s *Screen, left, top, w, h int) [3]byte {
	counts := map[[3]byte]int{}
	var best [3]byte
	for y := top; y < top+h; y++ {
		for x := left; x < left+w; x++ {
			c := s.at(x, y)
			counts[c]++
			if counts[c] > counts[best] {
				best = c
			}
		}
	}
	return best
}
