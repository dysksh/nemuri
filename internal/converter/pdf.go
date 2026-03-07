package converter

import (
	"bytes"
	"context"
	"fmt"
	"html/template"
	"os/exec"
	"strings"
	"time"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/renderer/html"
)

const wkhtmltopdfTimeout = 60 * time.Second

// MaxMarkdownSize is the maximum Markdown source size accepted for PDF conversion (2 MiB).
const MaxMarkdownSize = 2 << 20

const htmlTemplateStr = `<!DOCTYPE html>
<html lang="ja">
<head>
<meta charset="UTF-8">
<style>
body {
  font-family: "Noto Sans CJK JP", "Hiragino Sans", "Yu Gothic", sans-serif;
  font-size: 12pt;
  line-height: 1.8;
  color: #333;
  max-width: 210mm;
  margin: 0 auto;
  padding: 20mm 15mm;
}
h1 { font-size: 24pt; border-bottom: 2px solid #333; padding-bottom: 8px; }
h2 { font-size: 18pt; border-bottom: 1px solid #ccc; padding-bottom: 4px; }
h3 { font-size: 14pt; }
code {
  background: #f5f5f5;
  padding: 2px 6px;
  border-radius: 3px;
  font-size: 10pt;
}
pre {
  background: #f5f5f5;
  padding: 12px;
  border-radius: 4px;
  overflow-x: auto;
}
pre code { background: none; padding: 0; }
table { border-collapse: collapse; width: 100%; margin: 1em 0; }
th, td { border: 1px solid #ccc; padding: 8px 12px; text-align: left; }
th { background: #f0f0f0; }
blockquote {
  border-left: 4px solid #ccc;
  margin: 1em 0;
  padding: 0.5em 1em;
  color: #666;
}
</style>
</head>
<body>
{{.Body}}
</body>
</html>`

var (
	md      goldmark.Markdown
	htmlTpl *template.Template
)

func init() {
	// NOTE: Do NOT add html.WithUnsafe() — goldmark's default HTML sanitisation
	// is a defence-in-depth layer that complements wkhtmltopdf's
	// --disable-javascript / --disable-local-file-access flags.
	md = goldmark.New(
		goldmark.WithExtensions(
			extension.GFM,
			extension.Typographer,
		),
		goldmark.WithRendererOptions(
			html.WithXHTML(),
		),
	)
	htmlTpl = template.Must(template.New("doc").Parse(htmlTemplateStr))
}

// MarkdownToHTML converts Markdown source to a standalone HTML document.
func MarkdownToHTML(src []byte) ([]byte, error) {
	var body bytes.Buffer
	if err := md.Convert(src, &body); err != nil {
		return nil, fmt.Errorf("goldmark convert: %w", err)
	}

	var out bytes.Buffer
	if err := htmlTpl.Execute(&out, struct{ Body template.HTML }{
		Body: template.HTML(body.String()),
	}); err != nil {
		return nil, fmt.Errorf("execute html template: %w", err)
	}
	return out.Bytes(), nil
}

// HTMLToPDF converts HTML to PDF using wkhtmltopdf.
func HTMLToPDF(ctx context.Context, htmlData []byte) ([]byte, error) {
	timeoutCtx, cancel := context.WithTimeout(ctx, wkhtmltopdfTimeout)
	defer cancel()

	cmd := exec.CommandContext(timeoutCtx, "wkhtmltopdf",
		"--quiet",
		"--encoding", "UTF-8",
		"--page-size", "A4",
		"--margin-top", "20mm",
		"--margin-bottom", "20mm",
		"--margin-left", "15mm",
		"--margin-right", "15mm",
		"--disable-javascript",
		"--disable-local-file-access",
		"--footer-center", "[page] / [topage]",
		"--footer-font-size", "9",
		"-", "-", // stdin → stdout
	)
	cmd.Stdin = bytes.NewReader(htmlData)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("wkhtmltopdf: %w: %s", err, stderr.String())
	}
	return stdout.Bytes(), nil
}

// MarkdownToPDF converts Markdown source to PDF.
func MarkdownToPDF(ctx context.Context, src []byte) ([]byte, error) {
	if len(src) > MaxMarkdownSize {
		return nil, fmt.Errorf("markdown source too large for PDF conversion: %d bytes (max %d)", len(src), MaxMarkdownSize)
	}
	htmlData, err := MarkdownToHTML(src)
	if err != nil {
		return nil, err
	}
	return HTMLToPDF(ctx, htmlData)
}

// IsMarkdown returns true if the filename has a Markdown extension.
func IsMarkdown(filename string) bool {
	lower := strings.ToLower(filename)
	return strings.HasSuffix(lower, ".md") || strings.HasSuffix(lower, ".markdown")
}

// PDFFilename returns the corresponding PDF filename for a Markdown file.
func PDFFilename(mdFilename string) string {
	lower := strings.ToLower(mdFilename)
	for _, ext := range []string{".markdown", ".md"} {
		if strings.HasSuffix(lower, ext) {
			return mdFilename[:len(mdFilename)-len(ext)] + ".pdf"
		}
	}
	return mdFilename + ".pdf"
}

// Available returns true if wkhtmltopdf is installed and callable.
func Available() bool {
	_, err := exec.LookPath("wkhtmltopdf")
	return err == nil
}
