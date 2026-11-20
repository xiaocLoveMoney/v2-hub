package service

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"v2-hub-server/internal/manager/objectstore"
)

var (
	pngHeader  = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	jpegHeader = []byte("\xff\xd8\xff\xe0\x00\x10JFIF\x00")
	gifHeader  = []byte("GIF89a\x01\x00\x01\x00")
	webpHeader = []byte("RIFF\x24\x00\x00\x00WEBPVP8 ")
	// uploadURLPattern 与 Caddyfile 里 @upload 的路径形状一致（Ruling-89）。
	uploadURLPattern = regexp.MustCompile(`^/uploads/[0-9]{6}/[0-9a-f]{32}\.(png|jpg|gif|webp)$`)
)

type fakeObjects struct {
	puts []objectstore.Object
	err  error
}

func (f *fakeObjects) Put(_ context.Context, obj objectstore.Object) error {
	if f.err != nil {
		return f.err
	}
	f.puts = append(f.puts, obj)
	return nil
}

func testUploadConfig(now func() time.Time) UploadConfig {
	return UploadConfig{MaxBytes: 64, Limit: 2, Window: time.Hour, Now: now}
}

func TestUploadImageRejectsBeforeStoring(t *testing.T) {
	actor := &AdminPrincipal{AdminID: 1}
	objects := &fakeObjects{}
	svc := NewUploadService(nil, objects, testUploadConfig(nil))
	tests := []struct {
		name string
		data []byte
		code Code
	}{
		{"empty", nil, CodeInvalidArgument},
		{"too large", append(append([]byte{}, pngHeader...), make([]byte, 64)...), CodeInvalidArgument},
		{"svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`), CodeInvalidArgument},
		{"html", []byte("<!DOCTYPE html><html><script>x</script></html>"), CodeInvalidArgument},
		{"text named png", []byte("just some text"), CodeInvalidArgument},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := svc.UploadImage(context.Background(), actor, tt.data, nil)
			var se *Error
			if !errors.As(err, &se) || se.Code != tt.code {
				t.Fatalf("err = %v, want %s", err, tt.code)
			}
		})
	}
	if len(objects.puts) != 0 {
		t.Fatalf("rejected files must not be stored: %d", len(objects.puts))
	}
}

func TestUploadImageDisabledWithoutStore(t *testing.T) {
	svc := NewUploadService(nil, nil, testUploadConfig(nil))
	_, err := svc.UploadImage(context.Background(), &AdminPrincipal{AdminID: 1}, pngHeader, nil)
	if !errors.Is(err, ErrFeatureDisabled) {
		t.Fatalf("err = %v, want feature_disabled", err)
	}
}

func TestUploadImageStoreFailureIsUpstream(t *testing.T) {
	objects := &fakeObjects{err: objectstore.ErrUnavailable}
	svc := NewUploadService(nil, objects, testUploadConfig(nil))
	_, err := svc.UploadImage(context.Background(), &AdminPrincipal{AdminID: 1}, pngHeader, nil)
	if !errors.Is(err, ErrUpstream) {
		t.Fatalf("err = %v, want upstream_unavailable", err)
	}
}

func TestUploadImageStoresAuditsAndLimits(t *testing.T) {
	f := newAccountFixture(t)
	ctx := context.Background()
	objects := &fakeObjects{}
	svc := NewUploadService(f.pool, objects, testUploadConfig(f.clock.now))

	wantTypes := []struct {
		data []byte
		typ  string
		ext  string
	}{
		{pngHeader, "image/png", ".png"},
		{jpegHeader, "image/jpeg", ".jpg"},
	}
	for _, w := range wantTypes {
		img, err := svc.UploadImage(ctx, f.actor, w.data, nil)
		if err != nil {
			t.Fatalf("UploadImage(%s): %v", w.typ, err)
		}
		if img.ContentType != w.typ || img.Size != int64(len(w.data)) || !strings.HasSuffix(img.URL, w.ext) || !uploadURLPattern.MatchString(img.URL) {
			t.Fatalf("image = %+v", img)
		}
		if !strings.HasPrefix(img.URL, UploadPublicPrefix+f.clock.now().UTC().Format(uploadKeyMonth)+"/") {
			t.Fatalf("url month prefix wrong: %s", img.URL)
		}
	}
	if len(objects.puts) != 2 || objects.puts[0].ContentType != "image/png" || objects.puts[0].CacheControl != uploadCacheControl {
		t.Fatalf("puts = %+v", objects.puts)
	}
	if objects.puts[0].Key == objects.puts[1].Key {
		t.Fatal("object names must be random")
	}

	var n int
	err := f.pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action = $1 AND target_type = $2 AND target_id = $3`,
		ActionUploadImage, targetUpload, objects.puts[0].Key).Scan(&n)
	if err != nil || n != 1 {
		t.Fatalf("audit rows = %d, %v", n, err)
	}

	// 每小时 2 张用完后被频控，窗口过后恢复；GIF / WebP 也收
	if _, err := svc.UploadImage(ctx, f.actor, gifHeader, nil); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("third upload err = %v, want rate_limited", err)
	}
	f.clock.advance(time.Hour + time.Second)
	for _, data := range [][]byte{gifHeader, webpHeader} {
		if _, err := svc.UploadImage(ctx, f.actor, data, nil); err != nil {
			t.Fatalf("after window: %v", err)
		}
	}
}
