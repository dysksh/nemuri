package converter

import (
	"strings"
	"testing"
)

func TestStripFrontmatter(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "with frontmatter",
			in:   "---\ntitle: \"Test\"\ndate: \"2025\"\n---\n# Hello\nWorld",
			want: "# Hello\nWorld",
		},
		{
			name: "no frontmatter",
			in:   "# Hello\nWorld",
			want: "# Hello\nWorld",
		},
		{
			name: "unclosed frontmatter",
			in:   "---\ntitle: \"Test\"\n# Hello",
			want: "---\ntitle: \"Test\"\n# Hello",
		},
		{
			name: "empty frontmatter",
			in:   "---\n---\n# Hello",
			want: "# Hello",
		},
		{
			name: "frontmatter with extra newlines",
			in:   "---\ntitle: \"Test\"\n---\n\n\n# Hello",
			want: "# Hello",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := string(stripFrontmatter([]byte(tt.in)))
			if got != tt.want {
				t.Errorf("stripFrontmatter():\ngot:  %q\nwant: %q", got, tt.want)
			}
		})
	}
}

func TestMarkdownToHTML_StripsFrontmatter(t *testing.T) {
	src := []byte("---\ntitle: \"ISUCON Guide\"\ndate: \"2025\"\n---\n# Hello\n\nContent here.")
	html, err := MarkdownToHTML(src)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	htmlStr := string(html)
	if strings.Contains(htmlStr, "title:") || strings.Contains(htmlStr, "ISUCON Guide") {
		t.Errorf("frontmatter should be stripped, but found in output:\n%s", htmlStr)
	}
	if !strings.Contains(htmlStr, "<h1>Hello</h1>") {
		t.Errorf("expected <h1>Hello</h1> in output:\n%s", htmlStr)
	}
	if !strings.Contains(htmlStr, "Content here.") {
		t.Errorf("expected content in output:\n%s", htmlStr)
	}
}
