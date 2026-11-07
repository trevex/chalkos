package layer

import "fmt"

// Count is n and the noun, in the plural unless n is one.
func Count[N ~int | ~uint32 | ~uint64](n N, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
