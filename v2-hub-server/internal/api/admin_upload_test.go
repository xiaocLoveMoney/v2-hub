package api

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"v2-hub-server/internal/service"
)

const testUploadMax = 32

type fakeUploads struct {
	got   []byte
	calls int
}

func (f *fakeUploads) MaxBytes() int64 { return testUploadMax }

func (f *fakeUploads) UploadImage(_ context.Context, _ *service.AdminPrincipal, data []byte, _ *netip.Addr) (*service.UploadedImage, error) {
	f.calls++
	f.got = data
	return &service.UploadedImage{URL: "/uploads/202611/0123456789abcdef0123456789abcdef.png", ContentType: "image/png", Size: int64(len(data))}, nil
}

// multipartBody 依次写入文件字段（字段名, 内容），返回请求体与 Content-Type。
func multipartBody(t *testing.T, fields [][2]string) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for _, f := range fields {
		w, err := mw.CreateFormFile(f[0], "shot.png")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(f[1])); err != nil {
			t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf, mw.FormDataContentType()
}

func doUpload(t *testing.T, h *handlers, body *bytes.Buffer, contentType string) (*httptest.ResponseRecorder, map[string]json.RawMessage) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/admin/uploads", body)
	req.Header.Set(headerContent, contentType)
	rec := httptest.NewRecorder()
	h.uploadImage(rec, req, &service.AdminPrincipal{AdminID: 1})
	var env map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode envelope %q: %v", rec.Body.String(), err)
	}
	return rec, env
}

func TestUploadImageReadsOnlyFileField(t *testing.T) {
	fake := &fakeUploads{}
	h := &handlers{uploads: fake}
	body, ct := multipartBody(t, [][2]string{{"other", "ignored"}, {uploadFormField, "\x89PNG-data"}})
	rec, env := doUpload(t, h, body, ct)
	if rec.Code != http.StatusOK || string(fake.got) != "\x89PNG-data" {
		t.Fatalf("status = %d, got = %q, body = %s", rec.Code, fake.got, rec.Body.String())
	}
	var vo uploadedImageVO
	if err := json.Unmarshal(env["data"], &vo); err != nil || !strings.HasPrefix(vo.URL, "/uploads/") || vo.ContentType != "image/png" {
		t.Fatalf("data = %s, %v", env["data"], err)
	}
}

func TestUploadImageRejectsBadRequests(t *testing.T) {
	tests := []struct {
		name string
		body func(t *testing.T) (*bytes.Buffer, string)
	}{
		{"json body", func(_ *testing.T) (*bytes.Buffer, string) {
			return bytes.NewBufferString(`{"file":"x"}`), contentTypeJSON
		}},
		{"no file field", func(t *testing.T) (*bytes.Buffer, string) {
			return multipartBody(t, [][2]string{{"image", "\x89PNG"}})
		}},
		{"body over limit", func(t *testing.T) (*bytes.Buffer, string) {
			return multipartBody(t, [][2]string{{"other", strings.Repeat("x", uploadMultipartOverhead+testUploadMax+1)}, {uploadFormField, "\x89PNG"}})
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeUploads{}
			body, ct := tt.body(t)
			rec, _ := doUpload(t, &handlers{uploads: fake}, body, ct)
			if rec.Code != http.StatusBadRequest || fake.calls != 0 {
				t.Fatalf("status = %d, calls = %d, body = %s", rec.Code, fake.calls, rec.Body.String())
			}
		})
	}
}

func TestUploadImagePassesOversizeToService(t *testing.T) {
	fake := &fakeUploads{}
	body, ct := multipartBody(t, [][2]string{{uploadFormField, strings.Repeat("x", testUploadMax*2)}})
	rec, _ := doUpload(t, &handlers{uploads: fake}, body, ct)
	// handler 只读到上限 + 1 字节，超限的判定与提示交给 service
	if rec.Code != http.StatusOK || len(fake.got) != testUploadMax+1 {
		t.Fatalf("status = %d, read = %d", rec.Code, len(fake.got))
	}
}
