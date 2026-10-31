package files

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"os"

	"github.com/lyeith/codexpro/go/internal/wire"
)

func imageInfo(b []byte) (string, any, any, error) {
	if len(b) >= 24 && bytes.Equal(b[:8], []byte{137, 80, 78, 71, 13, 10, 26, 10}) {
		return "image/png", int(binary.BigEndian.Uint32(b[16:20])), int(binary.BigEndian.Uint32(b[20:24])), nil
	}
	if len(b) >= 10 && (string(b[:6]) == "GIF87a" || string(b[:6]) == "GIF89a") {
		return "image/gif", int(binary.LittleEndian.Uint16(b[6:8])), int(binary.LittleEndian.Uint16(b[8:10])), nil
	}
	if len(b) >= 4 && b[0] == 0xff && b[1] == 0xd8 && b[2] == 0xff {
		for offset := 2; offset+9 < len(b); {
			if b[offset] != 0xff {
				offset++
				continue
			}
			marker := b[offset+1]
			offset += 2
			if marker == 0xd8 || marker == 0xd9 {
				continue
			}
			if offset+2 > len(b) {
				break
			}
			n := int(binary.BigEndian.Uint16(b[offset : offset+2]))
			if n < 2 || offset+n > len(b) {
				break
			}
			if marker >= 0xc0 && marker <= 0xc3 || marker >= 0xc5 && marker <= 0xc7 || marker >= 0xc9 && marker <= 0xcb || marker >= 0xcd && marker <= 0xcf {
				return "image/jpeg", int(binary.BigEndian.Uint16(b[offset+5 : offset+7])), int(binary.BigEndian.Uint16(b[offset+3 : offset+5])), nil
			}
			offset += n
		}
		return "image/jpeg", nil, nil, nil
	}
	if len(b) >= 16 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP" {
		if string(b[12:16]) == "VP8X" && len(b) >= 30 {
			return "image/webp", 1 + int(b[24]) + int(b[25])<<8 + int(b[26])<<16, 1 + int(b[27]) + int(b[28])<<8 + int(b[29])<<16, nil
		}
		return "image/webp", nil, nil, nil
	}
	return "", nil, nil, wire.Errorf("", "Unsupported image format. Use PNG, JPEG, GIF, or WebP.")
}
func (s *Service) image(ctx context.Context, scope wire.Scope, w wire.Workspace, a map[string]any) (wire.Result, error) {
	abs, rel, e := s.ResolvePath(w, wire.String(a, "path", ""), false)
	if e != nil {
		return wire.Result{}, e
	}
	st, e := os.Stat(abs)
	if e != nil {
		return wire.Result{}, FileError(e, "stat", abs)
	}
	if !st.Mode().IsRegular() {
		return wire.Result{}, fmt.Errorf("Not a file: %s", rel)
	}
	limit := min(2000000, wire.Int(a, "max_bytes", max(s.Config.Int("maxReadBytes", 180000), 1000000)))
	if st.Size() > int64(limit) {
		return wire.Result{}, fmt.Errorf("Image is too large (%d bytes). Limit: %d bytes.", st.Size(), limit)
	}
	b, e := os.ReadFile(abs)
	if e != nil {
		return wire.Result{}, FileError(e, "open", abs)
	}
	mime, width, height, e := imageInfo(b)
	if e != nil {
		return wire.Result{}, e
	}
	dimensions := "unknown"
	if wi, ok := width.(int); ok && wi != 0 {
		if he, ok := height.(int); ok && he != 0 {
			dimensions = fmt.Sprintf("%dx%d", wi, he)
		}
	}
	data := Base(w)
	data["path"] = rel
	data["mime_type"] = mime
	data["width"] = width
	data["height"] = height
	data["bytes"] = len(b)
	data["sha256"] = SHABytes(b)
	data["codexpro_tool"] = "view_image"
	data["codexpro_title"] = "View Image"
	text := fmt.Sprintf("Image: %s\nType: %s\nDimensions: %s\nBytes: %d\nSHA-256: %s", rel, mime, dimensions, len(b), SHABytes(b))
	return wire.Result{Content: []wire.Content{{"type": "text", "text": text}, {"type": "image", "data": base64.StdEncoding.EncodeToString(b), "mimeType": mime}}, StructuredContent: data}, ctx.Err()
}
func detectMime(b []byte) string {
	if len(b) >= 8 && bytes.Equal(b[:8], []byte{137, 80, 78, 71, 13, 10, 26, 10}) {
		return "image/png"
	}
	if len(b) >= 3 && b[0] == 0xff && b[1] == 0xd8 && b[2] == 0xff {
		return "image/jpeg"
	}
	if len(b) >= 6 && (string(b[:6]) == "GIF87a" || string(b[:6]) == "GIF89a") {
		return "image/gif"
	}
	if len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP" {
		return "image/webp"
	}
	if len(b) >= 5 && string(b[:5]) == "%PDF-" {
		return "application/pdf"
	}
	if len(b) >= 4 && b[0] == 0x50 && b[1] == 0x4b && (b[2] == 3 || b[2] == 5 || b[2] == 7) && b[3] == 4 {
		return "application/zip"
	}
	if len(b) >= 2 && b[0] == 0x1f && b[1] == 0x8b {
		return "application/gzip"
	}
	return ""
}
