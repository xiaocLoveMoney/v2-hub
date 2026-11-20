package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/netip"
	"time"

	"v2-hub-server/internal/service"
)

// 公告 / 教程图片上传（docs/03 §5.9，Ruling-89），对应 hub-api.yaml POST /api/admin/uploads。

const (
	// uploadFormField 是 multipart 里唯一认的字段名。
	uploadFormField = "file"
	// uploadMultipartOverhead 是请求体在图片之外允许的余量（分隔符、各部分的头）。
	uploadMultipartOverhead = 64 << 10
	// uploadIOTimeout 放宽单次上传的读写时限：服务器默认的读超时（10 秒）对慢速上行的几 MiB 图片不够。
	uploadIOTimeout     = 2 * time.Minute
	mediaTypeMultipart  = "multipart/form-data"
	msgUploadNotForm    = "请以 multipart/form-data 上传，字段名为 file"
	msgUploadNoFile     = "请选择要上传的图片"
	msgUploadBadRequest = "上传内容不完整或超过大小上限"
)

// UploadManager 是上传接口需要的业务能力（由 service.UploadService 实现）。
type UploadManager interface {
	MaxBytes() int64
	UploadImage(ctx context.Context, actor *service.AdminPrincipal, data []byte, ip *netip.Addr) (*service.UploadedImage, error)
}

// uploadedImageVO 对应 hub-api.yaml UploadedImage。
type uploadedImageVO struct {
	URL         string `json:"url"`
	ContentType string `json:"contentType"`
	Size        int64  `json:"size"`
}

func (h *handlers) uploadImage(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	data, err := h.readUploadFile(w, r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	img, err := h.uploads.UploadImage(r.Context(), p, data, clientIP(r, h.trustedProxies))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, uploadedImageVO{URL: img.URL, ContentType: img.ContentType, Size: img.Size})
}

// readUploadFile 流式读 multipart，只取 file 字段，最多读到上限 + 1 字节（超限由 service 判定）；其余字段跳过。
func (h *handlers) readUploadFile(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get(headerContent))
	if err != nil || mediaType != mediaTypeMultipart {
		return nil, badRequest(msgUploadNotForm)
	}
	maxBytes := h.uploads.MaxBytes()
	rc := http.NewResponseController(w)
	deadline := time.Now().Add(uploadIOTimeout)
	if err := rc.SetReadDeadline(deadline); err != nil {
		slog.Warn("extend upload read deadline", "err", err)
	}
	if err := rc.SetWriteDeadline(deadline); err != nil {
		slog.Warn("extend upload write deadline", "err", err)
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes+uploadMultipartOverhead)
	reader, err := r.MultipartReader()
	if err != nil {
		return nil, badRequest(msgUploadNotForm)
	}
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			return nil, badRequest(msgUploadNoFile)
		}
		if err != nil {
			return nil, badRequest(msgUploadBadRequest)
		}
		if part.FormName() != uploadFormField {
			if err := drainPart(part); err != nil {
				return nil, badRequest(msgUploadBadRequest)
			}
			continue
		}
		data, err := io.ReadAll(io.LimitReader(part, maxBytes+1))
		if err != nil {
			return nil, badRequest(msgUploadBadRequest)
		}
		return data, nil
	}
}

func drainPart(part io.ReadCloser) error {
	if _, err := io.Copy(io.Discard, part); err != nil {
		return err
	}
	return part.Close()
}
