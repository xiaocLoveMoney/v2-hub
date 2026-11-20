package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"v2-hub-server/internal/manager/objectstore"
	"v2-hub-server/internal/manager/ratelimit"
)

// 公告 / 教程图片上传（docs/03 §5.9，Ruling-89）。

const (
	// ActionUploadImage 是上传图片的审计动作；目标是对象名。
	ActionUploadImage = "upload.image"
	targetUpload      = "upload"

	// UploadPublicPrefix 是图片对外的站内路径前缀，Caddy 按它转发到 MinIO 的桶（docs/03 §5.9）。
	UploadPublicPrefix = "/uploads/"
	// uploadCacheControl：对象名随机且不覆盖，内容永不变，浏览器与 CDN 可长期缓存。
	uploadCacheControl = "public, max-age=31536000, immutable"
	uploadKeyMonth     = "200601"
	uploadNameBytes    = 16
	uploadGuardMaxKeys = 1000
	bytesPerMiB        = 1 << 20

	msgUploadDisabled    = "图片上传未配置"
	msgUploadEmpty       = "请选择要上传的图片"
	msgUploadTooLarge    = "图片不能超过 %d MiB"
	msgUploadBadType     = "只支持 PNG、JPEG、GIF、WebP 图片"
	msgUploadUnavailable = "图片存储暂时不可用，请稍后再试"
)

// uploadImageTypes 是允许的图片类型（按 http.DetectContentType 的结果）到扩展名；不收 SVG（可带脚本）。
var uploadImageTypes = map[string]string{
	"image/png":  "png",
	"image/jpeg": "jpg",
	"image/gif":  "gif",
	"image/webp": "webp",
}

// UploadConfig 是图片上传的限制。
type UploadConfig struct {
	// MaxBytes 是单张上限；Limit / Window 是每个管理员的上传频率。
	MaxBytes int64
	Limit    int
	Window   time.Duration
	// Now 为 nil 时用 time.Now。
	Now func() time.Time
}

// UploadedImage 是一次上传的结果。
type UploadedImage struct {
	// URL 是站内路径，形如 /uploads/202611/<32 位十六进制>.png。
	URL         string
	ContentType string
	Size        int64
}

// UploadService 负责公告 / 教程图片的上传。
type UploadService struct {
	store   store
	objects objectstore.Store
	cfg     UploadConfig
	guard   *ratelimit.FailureGuard
	now     func() time.Time
}

// NewUploadService 创建上传服务；objects 为 nil 表示未配置存储，上传返回 feature_disabled。
func NewUploadService(pool *pgxpool.Pool, objects objectstore.Store, cfg UploadConfig) *UploadService {
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	guard := ratelimit.NewFailureGuard(ratelimit.FailureGuardConfig{
		MaxFailures: cfg.Limit, Window: cfg.Window, Lockout: cfg.Window, MaxKeys: uploadGuardMaxKeys,
	}, now)
	return &UploadService{store: newStore(pool), objects: objects, cfg: cfg, guard: guard, now: now}
}

// MaxBytes 返回单张上限，供 handler 先卡住请求体。
func (s *UploadService) MaxBytes() int64 { return s.cfg.MaxBytes }

// UploadImage 校验并保存一张图片，记审计。
// 错误：CodeFeatureDisabled（未配置存储）、CodeInvalidArgument（空、超限、类型不对）、CodeRateLimited、CodeUpstream（存储写入失败）。
func (s *UploadService) UploadImage(ctx context.Context, actor *AdminPrincipal, data []byte, ip *netip.Addr) (*UploadedImage, error) {
	if s.objects == nil {
		return nil, newError(CodeFeatureDisabled, msgUploadDisabled)
	}
	if len(data) == 0 {
		return nil, newError(CodeInvalidArgument, msgUploadEmpty)
	}
	if int64(len(data)) > s.cfg.MaxBytes {
		return nil, newError(CodeInvalidArgument, fmt.Sprintf(msgUploadTooLarge, s.cfg.MaxBytes/bytesPerMiB))
	}
	contentType := http.DetectContentType(data)
	ext, ok := uploadImageTypes[contentType]
	if !ok {
		return nil, newError(CodeInvalidArgument, msgUploadBadType)
	}
	if err := allow(s.guard, actor.AdminID); err != nil {
		return nil, err
	}
	key, err := s.newKey(ext)
	if err != nil {
		return nil, err
	}
	err = s.objects.Put(ctx, objectstore.Object{Key: key, Body: data, ContentType: contentType, CacheControl: uploadCacheControl})
	if err != nil {
		if errors.Is(err, objectstore.ErrUnavailable) {
			slog.Error("upload image", "adminID", actor.AdminID, "key", key, "size", len(data), "err", err)
			return nil, newError(CodeUpstream, msgUploadUnavailable)
		}
		return nil, fmt.Errorf("upload image %s: %w", key, err)
	}
	size := int64(len(data))
	entry := adminAudit(actor, ActionUploadImage, targetUpload, 0, ip, map[string]any{"size": size, "contentType": contentType})
	entry.TargetID = key
	if err := writeAudit(ctx, s.store.q, entry); err != nil {
		return nil, err
	}
	return &UploadedImage{URL: UploadPublicPrefix + key, ContentType: contentType, Size: size}, nil
}

// newKey 生成对象名：年月/32 位随机十六进制.扩展名，不含原文件名。
func (s *UploadService) newKey(ext string) (string, error) {
	raw := make([]byte, uploadNameBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("random upload name: %w", err)
	}
	return s.now().UTC().Format(uploadKeyMonth) + "/" + hex.EncodeToString(raw) + "." + ext, nil
}
