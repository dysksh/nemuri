package executor

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/nemuri/nemuri/internal/agent"
	"github.com/nemuri/nemuri/internal/converter"
	"github.com/nemuri/nemuri/internal/state"
	"github.com/nemuri/nemuri/internal/storage"
)

func (e *Executor) executeFileJob(ctx context.Context, job *state.Job, resp agent.AgentResponse) error {
	if e.Storage == nil {
		return fmt.Errorf("file output requested but S3 storage is not configured")
	}
	if len(resp.Files) == 0 {
		return fmt.Errorf("file response has no files")
	}

	slog.Info("uploading file deliverables", "job_id", job.JobID, "files", len(resp.Files))

	canPDF := converter.Available()

	var lines []string
	for i, f := range resp.Files {
		if f.Name == "" {
			return fmt.Errorf("file response contains entry with empty name")
		}
		contentBytes := []byte(f.Content)
		if err := e.Storage.UploadOutput(ctx, job.JobID, f.Name, contentBytes); err != nil {
			return fmt.Errorf("upload %s: %w", f.Name, err)
		}

		// Convert Markdown files to PDF; show PDF link instead of md.
		pdfDone := false
		if canPDF && converter.IsMarkdown(f.Name) {
			if pdfLine, ok := convertAndUploadPDF(ctx, job.JobID, f.Name, contentBytes, e.Storage); ok {
				lines = append(lines, pdfLine)
				pdfDone = true
			}
		}
		if !pdfDone {
			url, err := e.Storage.GetOutputPresignedURL(ctx, job.JobID, f.Name)
			if err != nil {
				return fmt.Errorf("presign %s: %w", f.Name, err)
			}
			lines = append(lines, fmt.Sprintf("`%s`: %s", f.Name, url))
		}

		// Release content to allow GC to reclaim memory.
		resp.Files[i].Content = ""
	}

	message := fmt.Sprintf("ファイルを生成しました（24時間有効）:\n%s", strings.Join(lines, "\n"))

	channelID := job.ChannelID
	if job.ThreadID != "" {
		channelID = job.ThreadID
	}
	return e.Discord.SendResult(ctx, job.ApplicationID, job.InteractionToken, channelID, message)
}

func convertAndUploadPDF(ctx context.Context, jobID, fileName string, mdContent []byte, storageClient *storage.Client) (string, bool) {
	pdfName := converter.PDFFilename(fileName)
	pdfData, err := converter.MarkdownToPDF(ctx, mdContent)
	if err != nil {
		slog.Warn("PDF conversion failed, skipping", "file", fileName, "error", err)
		return "", false
	}
	if err := storageClient.UploadOutput(ctx, jobID, pdfName, pdfData); err != nil {
		slog.Warn("PDF upload failed, skipping", "file", pdfName, "error", err)
		return "", false
	}
	pdfURL, err := storageClient.GetOutputPresignedURL(ctx, jobID, pdfName)
	if err != nil {
		slog.Warn("PDF presign failed, skipping", "file", pdfName, "error", err)
		return "", false
	}
	slog.Info("PDF generated", "job_id", jobID, "file", pdfName)
	return fmt.Sprintf("`%s` (PDF): %s", pdfName, pdfURL), true
}
