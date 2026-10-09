package tools

import (
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"aivory/server/internal/llm"
)

const webPageImageLimit = 8

type webPageImage struct {
	URL       string `json:"image_url"`
	Title     string `json:"title,omitempty"`
	SourceURL string `json:"source_url"`
}

type fetchedPage struct {
	Text   string
	Images []webPageImage
}

// Image metadata is extracted from the existing page response, without any
// extra network requests. Limits bound both extraction work and model tokens.
func pageImageURL(raw, sourceURL string) string {
	raw = strings.TrimSpace(html.UnescapeString(raw))
	if raw == "" || len(raw) > 2048 || strings.HasPrefix(raw, "#") {
		return ""
	}
	base, err := url.Parse(sourceURL)
	if err != nil {
		return ""
	}
	reference, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return searchImageURL(base.ResolveReference(reference).String())
}

func appendPageImage(images []webPageImage, seen map[string]bool, raw, title, sourceURL string) []webPageImage {
	if len(images) >= webPageImageLimit {
		return images
	}
	imageURL := pageImageURL(raw, sourceURL)
	if imageURL == "" || seen[imageURL] {
		return images
	}
	seen[imageURL] = true
	return append(images, webPageImage{URL: imageURL, Title: cleanSnippet(html.UnescapeString(title)), SourceURL: sourceURL})
}

var pageImageTagRe = regexp.MustCompile(`(?is)<img\b[^>]*>`)
var pageImageAttributeRe = regexp.MustCompile(`(?is)\b(src|data-src|data-original|alt|width|height)\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))`)

func readablePageHTML(body string) string {
	body = scriptStyleRe.ReplaceAllString(body, " ")
	if match := readabilityContainerRe.FindStringSubmatch(body); len(match) >= 3 {
		return match[2]
	}
	return body
}

func htmlPageImages(body, sourceURL string) []webPageImage {
	var images []webPageImage
	seen := map[string]bool{}
	for _, tag := range pageImageTagRe.FindAllString(readablePageHTML(body), 128) {
		attributes := map[string]string{}
		for _, match := range pageImageAttributeRe.FindAllStringSubmatch(tag, -1) {
			value := match[2]
			if value == "" {
				value = match[3]
			}
			if value == "" {
				value = match[4]
			}
			attributes[strings.ToLower(match[1])] = value
		}
		// Avoid including explicitly tiny tracking/decorative pixels.
		tiny := false
		for _, dimension := range []string{"width", "height"} {
			if size, err := strconv.Atoi(attributes[dimension]); err == nil && size >= 0 && size <= 2 {
				tiny = true
			}
		}
		if tiny {
			continue
		}
		for _, attribute := range []string{"data-src", "data-original", "src"} {
			if imageURL := pageImageURL(attributes[attribute], sourceURL); imageURL != "" {
				images = appendPageImage(images, seen, imageURL, attributes["alt"], sourceURL)
				break
			}
		}
		if len(images) >= webPageImageLimit {
			break
		}
	}
	return images
}

var markdownImageStartRe = regexp.MustCompile(`!\[((?:\\.|[^\]\\\r\n]){0,512})\]\(`)
var markdownImageEscapeRe = regexp.MustCompile(`\\([\\()\[\]<> ])`)
var markdownImageCodeBlockRe = regexp.MustCompile("(?ms)^ {0,3}(?:`{3,}|~{3,})[^\\n]*\\n.*?^ {0,3}(?:`{3,}|~{3,})[ \\t]*(?:\\n|$)")
var markdownImageInlineCodeRe = regexp.MustCompile("`[^`\\r\\n]*`")

// Common Jina/compatible readers emit Markdown images. Handle escaped and
// balanced parentheses (common in CDN filenames) and optional Markdown titles.
func markdownPageImages(body, sourceURL string) []webPageImage {
	body = markdownImageInlineCodeRe.ReplaceAllString(markdownImageCodeBlockRe.ReplaceAllString(body, " "), " ")
	var images []webPageImage
	seen := map[string]bool{}
	for _, match := range markdownImageStartRe.FindAllStringSubmatchIndex(body, 128) {
		start := match[1]
		depth, end := 1, -1
		var quote byte
		for cursor := start; cursor < len(body) && cursor-start <= 4096; cursor++ {
			char := body[cursor]
			if char == '\\' {
				cursor++
				continue
			}
			if quote != 0 {
				if char == quote {
					quote = 0
				}
				continue
			}
			if (char == '"' || char == '\'') && cursor > start && (body[cursor-1] == ' ' || body[cursor-1] == '\t') {
				quote = char
				continue
			}
			if char == '(' {
				depth++
			}
			if char == ')' {
				depth--
				if depth == 0 {
					end = cursor
					break
				}
			}
		}
		if end < 0 {
			continue
		}
		destination := strings.TrimSpace(body[start:end])
		if strings.HasPrefix(destination, "<") {
			if close := strings.IndexByte(destination, '>'); close >= 0 {
				destination = destination[1:close]
			} else {
				continue
			}
		} else if space := strings.IndexFunc(destination, unicode.IsSpace); space >= 0 {
			destination = destination[:space]
		}
		destination = markdownImageEscapeRe.ReplaceAllString(destination, "$1")
		title := markdownImageEscapeRe.ReplaceAllString(body[match[2]:match[3]], "$1")
		images = appendPageImage(images, seen, destination, title, sourceURL)
		if len(images) >= webPageImageLimit {
			break
		}
	}
	// Some compatible readers return embedded HTML image tags in their Markdown.
	for _, image := range htmlPageImages(body, sourceURL) {
		images = appendPageImage(images, seen, image.URL, image.Title, sourceURL)
	}
	return images
}

func fetchedPageOutput(page fetchedPage, sourceURL string, showImages bool, offset int) (string, []llm.Citation) {
	if len(page.Images) == 0 {
		return page.Text, nil
	}
	var metadata strings.Builder
	var citations []llm.Citation
	metadata.WriteString("\n\nPage images (URLs/captions only, not visual verification):\n")
	if showImages {
		for _, image := range page.Images {
			index := offset + len(citations) + 1
			title := image.Title
			if title == "" {
				title = sourceURL
			}
			fmt.Fprintf(&metadata, "[%d] %s\nsource_url: %s\nimage_url: %s\n", index, title, image.SourceURL, image.URL)
			citations = append(citations, llm.Citation{ID: fmt.Sprintf("wf_%d", index), Index: index, Title: title, URL: image.SourceURL, ImageURL: image.URL, Source: "web", ImageDisplay: &showImages})
		}
	} else {
		encoded, _ := json.Marshal(page.Images)
		metadata.Write(encoded)
	}
	// Reserve room for the image metadata within the existing page-text budget.
	text := []rune(page.Text)
	budget := webFetchExtractedTextCharCap - len([]rune(metadata.String()))
	if budget < 0 {
		return page.Text, nil
	}
	if len(text) > budget {
		const marker = "\n…[truncated]"
		keep := budget - len([]rune(marker))
		if keep < 0 {
			keep = 0
		}
		page.Text = string(text[:keep]) + marker
	}
	return page.Text + metadata.String(), citations
}
