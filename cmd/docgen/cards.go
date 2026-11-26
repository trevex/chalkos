package main

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// A region of cards: the line that opens it names the pages, relative to the index page, and the
// line that closes it ends it. A grid of cards outside such a region is hand-written.
var (
	cardsRegion = regexp.MustCompile(`(?s)(<!-- docgen cards: ([^>]*?) -->\n).*?(<!-- end docgen cards -->)`)
	cardsGrid   = regexp.MustCompile(`<div\b[^>]*\bclass\s*=\s*["']\s*(?:grid\s+cards|cards\s+grid)\s*["']`)
	description = regexp.MustCompile(`(?m)^description:\s*"?(.*?)"?\s*$`)
)

// writeCards writes the index pages, by their paths below docs, to the same paths below out, with
// each region of cards filled with a card per page it names: the page's title, linked, and its
// description, from their front matter. A page is read below out when it is there, as the
// generated pages are, and below docs otherwise. An index page with a grid of cards outside a
// region fails.
func writeCards(out, docs string, pages []string) error {
	var errs []error
	for _, page := range pages {
		data, err := os.ReadFile(filepath.Join(docs, page))
		if err != nil {
			return err
		}
		text, err := fillCards(string(data), func(target string) (string, string, error) {
			return frontMatterOf(out, docs, path.Join(path.Dir(page), target))
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", page, err))
			continue
		}
		dst := filepath.Join(out, page)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dst, []byte(text), 0o644); err != nil {
			return err
		}
	}
	return errors.Join(errs...)
}

// fillCards fills each region of cards of text with the cards of the pages it names, whose title
// and description lookup returns.
func fillCards(text string, lookup func(page string) (string, string, error)) (string, error) {
	var errs []error
	filled := cardsRegion.ReplaceAllStringFunc(text, func(region string) string {
		m := cardsRegion.FindStringSubmatch(region)
		var b strings.Builder
		b.WriteString(m[1])
		b.WriteString("<div class=\"grid cards\" markdown>\n\n")
		for _, page := range strings.Fields(m[2]) {
			title, desc, err := lookup(page)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			fmt.Fprintf(&b, "-   __[%s](%s)__\n\n    ---\n\n    %s\n\n", title, page, desc)
		}
		b.WriteString("</div>\n")
		b.WriteString(m[3])
		return b.String()
	})
	if outside := cardsGrid.FindAllStringIndex(cardsRegion.ReplaceAllString(filled, ""), -1); len(outside) > 0 {
		errs = append(errs, errors.New("a grid of cards is written by hand; name its pages in a region <!-- docgen cards: PAGE... --> ... <!-- end docgen cards --> instead"))
	}
	return filled, errors.Join(errs...)
}

// frontMatterOf is the title and description of the page at page, below out if it is there and
// below docs otherwise.
func frontMatterOf(out, docs, page string) (string, string, error) {
	data, err := os.ReadFile(filepath.Join(out, page))
	if errors.Is(err, os.ErrNotExist) {
		data, err = os.ReadFile(filepath.Join(docs, page))
	}
	if err != nil {
		return "", "", err
	}
	t, d := titleLine.FindSubmatch(data), description.FindSubmatch(data)
	if t == nil || d == nil {
		return "", "", fmt.Errorf("%s has no title or description", page)
	}
	return string(t[1]), string(d[1]), nil
}
