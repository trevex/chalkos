package lab

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// testGlyphs are the test font's bitmaps, and x one it lacks. Each covers less than half of its
// cell, as a real font's glyphs do, which the cell's background colour relies on.
var testGlyphs = map[rune][]byte{
	' ': {0x00, 0x00, 0x00, 0x00},
	'A': {0x40, 0xa0, 0xe0, 0x00}, // .X.. X.X. XXX. ....
	'B': {0xc0, 0xc0, 0x00, 0x00}, // XX.. XX.. .... ....
	'C': {0x60, 0x80, 0x60, 0x00}, // .XX. X... .XX. ....
	'x': {0x10, 0x00, 0x00, 0x00}, // ...X .... .... ....
}

// testFont is a PSF2 font of 4x4 glyphs, as kbd writes them: a blank, A, B, and one glyph for
// both a non-ASCII character and C, which the text reads as C.
func testFont(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	for _, v := range []uint32{0x864ab572, 0, 32, 1, 4, 4, 4, 4} {
		binary.Write(&b, binary.LittleEndian, v)
	}
	for _, r := range []rune{' ', 'A', 'B', 'C'} {
		b.Write(testGlyphs[r])
	}
	for _, runes := range [][]rune{{' '}, {'A'}, {'B'}, {'Ç', 'C'}} {
		for _, r := range runes {
			b.WriteString(string(r))
		}
		b.WriteByte(0xff)
	}
	return b.Bytes()
}

// render draws rows of text with testGlyphs, grey on black, the cells of reversed black on grey.
func render(rows []string, reversed map[[2]int]bool) *Screen {
	fg, bg := [3]byte{0xaa, 0xaa, 0xaa}, [3]byte{0, 0, 0}
	s := &Screen{Width: 4 * len(rows[0]), Height: 4 * len(rows)}
	s.Pix = make([]byte, 3*s.Width*s.Height)
	for row, text := range rows {
		for col, r := range []rune(text) {
			g := testGlyphs[r]
			for y := range 4 {
				for x := range 4 {
					on := g[y]&(0x80>>x) != 0
					if reversed[[2]int{row, col}] {
						on = !on
					}
					c := bg
					if on {
						c = fg
					}
					copy(s.Pix[3*((4*row+y)*s.Width+4*col+x):], c[:])
				}
			}
		}
	}
	return s
}

func TestFontText(t *testing.T) {
	f, err := parsePSF(testFont(t))
	if err != nil {
		t.Fatal(err)
	}
	s := render([]string{"AB C", "C xA"}, map[[2]int]bool{{0, 1}: true})
	// A reversed cell reads as its character; one no glyph draws reads as ?.
	if got, want := f.Text(s), []string{"AB C", "C ?A"}; !slices.Equal(got, want) {
		t.Errorf("Text = %q, want %q", got, want)
	}
}

func TestReadPPM(t *testing.T) {
	data := append([]byte("P6\n2 1\n255\n"), 1, 2, 3, 4, 5, 6)
	s, err := ReadPPM(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if s.Width != 2 || s.Height != 1 || s.at(1, 0) != [3]byte{4, 5, 6} {
		t.Errorf("ReadPPM = %+v", s)
	}
	if _, err := ReadPPM(bytes.NewReader([]byte("P5\n2 1\n255\n"))); err == nil {
		t.Error("a grey PGM was read")
	}
}

// ReadPSF reads kbd's gzipped fonts as well as plain ones.
func TestReadPSFGzipped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "font.psfu.gz")
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	w.Write(testFont(t))
	w.Close()
	if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := ReadPSF(path)
	if err != nil {
		t.Fatal(err)
	}
	if f.Width != 4 || f.Height != 4 || len(f.chars) != 4 {
		t.Errorf("ReadPSF = %dx%d with %d glyphs", f.Width, f.Height, len(f.chars))
	}
	if _, err := parsePSF([]byte("not a font")); err == nil {
		t.Error("parsePSF read junk")
	}
}
