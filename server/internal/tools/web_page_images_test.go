package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"unicode/utf8"
)

func TestHTMLPageImagesResolveSourcesAndSkipUnsafeOrDecorativeImages(t *testing.T) {
	body := `<header><img src="/logo.png"></header><main>
	<p>Travel article</p><img src="../photos/temple(2).jpg?x=1&amp;y=2" alt="Temple &amp; gardens">
	<img src="data:image/gif,placeholder" data-src="//cdn.example.test/view.jpg" alt='Scenery'>
	<img src="https://cdn.example.test/view.jpg"><img src="/tracking.gif" width="1">
	<img src="javascript:alert(1)"><img src="file:///tmp/a.jpg"><img src="https://user:password@cdn.example.test/private.jpg">
	</main><footer><img src="/footer.jpg"></footer>`
	images := htmlPageImages(body, "https://example.test/articles/kyoto")
	if len(images) != 2 || images[0].URL != "https://example.test/photos/temple(2).jpg?x=1&y=2" || images[0].Title != "Temple & gardens" || images[1].URL != "https://cdn.example.test/view.jpg" {
		t.Fatalf("page images: %+v", images)
	}
	if images[0].SourceURL != "https://example.test/articles/kyoto" {
		t.Fatalf("source page lost: %+v", images[0])
	}
}

func TestJinaMarkdownImageMetadata(t *testing.T) {
	body := `Title: Kyoto
Markdown Content:
![Image 1: Temple](https://cdn.example.test/photo(2).jpg "Temple photo")
![Image 2: Garden](../garden.jpg)
![Duplicate](https://cdn.example.test/photo(2).jpg)
![Angle URL](<https://cdn.example.test/second.jpg> "A photo")
![Escaped](https://cdn.example.test/a\(3\).jpg)
![Unsafe](javascript:alert(1))
![Data](data:image/png;base64,aaa)
![Local](file:///tmp/photo.jpg)
<img src="/html-photo.jpg" alt="HTML caption">`
	body += "\n`![Code example](https://cdn.example.test/code.jpg)`\n```md\n![Code block](https://cdn.example.test/code-block.jpg)\n```\n"
	images := markdownPageImages(body, "https://example.test/articles/kyoto")
	if len(images) != 5 {
		t.Fatalf("reader images: %+v", images)
	}
	for index, expected := range []string{"https://cdn.example.test/photo(2).jpg", "https://example.test/garden.jpg", "https://cdn.example.test/second.jpg", "https://cdn.example.test/a(3).jpg", "https://example.test/html-photo.jpg"} {
		if images[index].URL != expected {
			t.Errorf("image %d=%q want %q", index, images[index].URL, expected)
		}
	}
}

func TestWebFetchKeepsImagesWithoutAutomaticallyShowingThem(t *testing.T) {
	var calls atomic.Int32
	tool := &webFetchTool{direct: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		return statusTransport{code: 200, body: `<article><p>Temple description.</p><img src="/photo.jpg" alt="Temple"></article>`}.RoundTrip(r)
	})}
	out, citations, err := tool.Execute(context.Background(), []byte(`{"url":"https://example.test/article"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(citations) != 0 || !strings.Contains(out, "Temple description.") || !strings.Contains(out, `"image_url":"https://example.test/photo.jpg"`) || !strings.Contains(out, `"source_url":"https://example.test/article"`) || calls.Load() != 1 {
		t.Fatalf("metadata/default/network calls=%s / %+v / %d", out, citations, calls.Load())
	}
}

func TestWebFetchShowsImagesFromJinaWithoutDownloadingThem(t *testing.T) {
	t.Setenv("AIVORY_TOOLS_WEB_FETCH_JINA_FALLBACK", "1")
	t.Setenv("AIVORY_TOOLS_WEB_FETCH_JINA_BASE", "https://reader.example.test")
	var calls atomic.Int32
	tool := &webFetchTool{direct: errTransport{}, reader: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		return statusTransport{code: 200, body: "# Temple\n![Garden](https://images.example.test/garden.jpg)\n![Temple](https://images.example.test/temple.jpg)"}.RoundTrip(r)
	})}
	out, citations, err := tool.Execute(context.Background(), []byte(`{"url":"http://1.1.1.1/article","show_images":true}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(citations) != 2 || citations[0].ImageURL != "https://images.example.test/garden.jpg" || citations[0].Title != "Garden" || citations[0].URL != "http://1.1.1.1/article" || citations[0].ImageDisplay == nil || !*citations[0].ImageDisplay || calls.Load() != 1 {
		t.Fatalf("reader gallery/network calls=%s / %+v / %d", out, citations, calls.Load())
	}
	if !strings.Contains(out, "[1] Garden") || !strings.Contains(out, "[2] Temple") {
		t.Fatalf("gallery citation markers missing: %s", out)
	}
}

func TestOriginImageWithoutTextDoesNotSuppressReaderFallback(t *testing.T) {
	t.Setenv("AIVORY_TOOLS_WEB_FETCH_JINA_FALLBACK", "1")
	t.Setenv("AIVORY_TOOLS_WEB_FETCH_JINA_BASE", "https://reader.example.test")
	tool := &webFetchTool{
		direct: statusTransport{code: 200, body: `<html><img src="/logo.png"><script>loadArticle()</script></html>`},
		reader: statusTransport{code: 200, body: "# Rendered article\nActual article text.\n![Article photo](https://images.test/article.jpg)"},
	}
	out, citations, err := tool.Execute(context.Background(), []byte(`{"url":"http://1.1.1.1/article","show_images":true}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Actual article text.") || len(citations) != 1 || citations[0].ImageURL != "https://images.test/article.jpg" {
		t.Fatalf("decorative origin image suppressed the reader: %s / %+v", out, citations)
	}
}

func TestWebFetchBatchImageCitationsAreNumberedAcrossPages(t *testing.T) {
	tool := &webFetchTool{direct: statusTransport{code: 200, body: `<article><p>Page body.</p><img src="/photo.jpg" alt="Photo"></article>`}}
	out, citations, err := tool.Execute(context.Background(), []byte(`{"urls":["https://first.example.test/page","https://second.example.test/page"],"show_images":true}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	var batch webFetchBatchResult
	if json.Unmarshal([]byte(out), &batch) != nil || len(citations) != 2 || citations[1].Index != 2 || citations[1].ImageURL != "https://second.example.test/photo.jpg" || !strings.Contains(batch.Items[1].Content, "[2] Photo") {
		t.Fatalf("batch image citations: %s / %+v", out, citations)
	}
}

func TestPageImageExtractionAndOutputStayBounded(t *testing.T) {
	var body strings.Builder
	for index := 0; index < 100; index++ {
		fmt.Fprintf(&body, "![%d](https://images.test/%d.jpg)\n", index, index)
	}
	images := markdownPageImages(body.String(), "https://example.test/page")
	if len(images) != webPageImageLimit {
		t.Fatalf("image count=%d", len(images))
	}
	out, _ := fetchedPageOutput(fetchedPage{Text: strings.Repeat("界", webFetchExtractedTextCharCap), Images: images}, "https://example.test/page", false, 0)
	if utf8.RuneCountInString(out) > webFetchExtractedTextCharCap || !strings.Contains(out, `"image_url":"https://images.test/7.jpg"`) || !strings.Contains(out, "[truncated]") {
		t.Fatalf("text/image budget lost: count=%d, tail=%s", utf8.RuneCountInString(out), out[len(out)-500:])
	}
}
