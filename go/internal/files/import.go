package files

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lyeith/codexpro/go/internal/wire"
)

func importBool(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
func privateIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return true
	}
	if v := ip.To4(); v != nil {
		return v[0] == 10 || v[0] == 127 || v[0] == 0 || v[0] == 169 && v[1] == 254 || v[0] == 172 && v[1] >= 16 && v[1] <= 31 || v[0] == 192 && v[1] == 168 || v[0] == 100 && v[1] >= 64 && v[1] <= 127 || v[0] >= 224
	}
	v := ip.To16()
	return v == nil || v[0]&0xfe == 0xfc
}
func (s *Service) safeImportURL(ctx context.Context, u *url.URL, loopback bool) error {
	if u.Scheme != "https" && !(loopback && u.Scheme == "http") {
		return wire.Errorf("", "Attachment download_url must use HTTPS.")
	}
	if u.User != nil {
		return fmt.Errorf("Attachment download_url must not include credentials.")
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return fmt.Errorf("Attachment download_url is missing a hostname.")
	}
	local := host == "localhost" || host == "127.0.0.1" || host == "::1"
	if local {
		if !loopback {
			return fmt.Errorf("Attachment download_url points to a blocked host.")
		}
	} else {
		hosts := []string{"files.oaiusercontent.com", "oaiusercontent.com", "chatgpt.com", "openai.com", "cdn.openai.com", "oaistatic.com"}
		extra := s.Config.String("importAllowedHosts", os.Getenv("CODEXPRO_IMPORT_ALLOWED_HOSTS"))
		hosts = append(hosts, strings.FieldsFunc(extra, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r' })...)
		ok := false
		host = strings.TrimSuffix(host, ".")
		for _, a := range hosts {
			a = strings.ToLower(strings.TrimSpace(a))
			if a != "" && (host == a || strings.HasSuffix(host, "."+a)) {
				ok = true
				break
			}
		}
		if !ok {
			return wire.Errorf("", "Attachment download_url host is not an approved ChatGPT file origin.")
		}
	}
	if ip := net.ParseIP(host); ip != nil {
		if privateIP(ip) && !(loopback && local) {
			return fmt.Errorf("Attachment download_url resolves to a blocked address.")
		}
		return nil
	}
	ips, e := net.DefaultResolver.LookupIPAddr(ctx, host)
	if e != nil || len(ips) == 0 {
		return fmt.Errorf("Attachment download_url hostname could not be resolved.")
	}
	for _, ip := range ips {
		if privateIP(ip.IP) && !(loopback && local) {
			return fmt.Errorf("Attachment download_url resolves to a blocked address.")
		}
	}
	return nil
}
func (s *Service) download(ctx context.Context, raw string, limit int) (string, int, string, error) {
	current, e := url.Parse(raw)
	if e != nil || current.Scheme == "" || current.Host == "" {
		return "", 0, "", fmt.Errorf("Invalid URL")
	}
	loopback := s.Config.Bool("importAllowLoopback", importBool(os.Getenv("CODEXPRO_IMPORT_ALLOW_LOOPBACK")))
	client := &http.Client{Timeout: 60 * time.Second, Transport: &http.Transport{DisableCompression: true}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	for redirect := 0; redirect <= 5; redirect++ {
		if e = s.safeImportURL(ctx, current, loopback); e != nil {
			return "", 0, "", e
		}
		req, e := http.NewRequestWithContext(ctx, http.MethodGet, current.String(), nil)
		if e != nil {
			return "", 0, "", e
		}
		req.Header.Set("Accept", "*/*")
		req.Header.Set("User-Agent", "codexpro-import/0.30")
		resp, e := client.Do(req)
		if e != nil {
			if ctx.Err() != nil {
				return "", 0, "", ctx.Err()
			}
			if ne, ok := e.(net.Error); ok && ne.Timeout() {
				return "", 0, "", fmt.Errorf("Attachment download timed out.")
			}
			return "", 0, "", e
		}
		switch resp.StatusCode {
		case 301, 302, 303, 307, 308:
			loc := resp.Header.Get("Location")
			resp.Body.Close()
			if loc == "" {
				return "", 0, "", fmt.Errorf("Attachment download redirected without a Location header.")
			}
			u, e := url.Parse(loc)
			if e != nil {
				return "", 0, "", e
			}
			current = current.ResolveReference(u)
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			resp.Body.Close()
			return "", 0, "", fmt.Errorf("Attachment download failed with HTTP %d.", resp.StatusCode)
		}
		if resp.ContentLength > int64(limit) {
			resp.Body.Close()
			return "", 0, "", fmt.Errorf("Attachment is too large. Limit: %d bytes.", limit)
		}
		f, e := createImportTemp()
		if e != nil {
			resp.Body.Close()
			return "", 0, "", e
		}
		n, copyErr := io.Copy(f, io.LimitReader(resp.Body, int64(limit)+1))
		resp.Body.Close()
		closeErr := f.Close()
		if copyErr != nil || closeErr != nil || n > int64(limit) {
			os.Remove(f.Name())
			if n > int64(limit) {
				return "", 0, "", fmt.Errorf("Attachment exceeded import size limit (%d bytes).", limit)
			}
			if copyErr != nil {
				return "", 0, "", copyErr
			}
			return "", 0, "", closeErr
		}
		mime := strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0])
		return f.Name(), int(n), mime, nil
	}
	return "", 0, "", fmt.Errorf("Attachment download exceeded redirect limit.")
}
func copyExclusive(src, dest string) error {
	in, e := os.Open(src)
	if e != nil {
		return e
	}
	defer in.Close()
	out, e := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0666)
	if e != nil {
		return e
	}
	_, e = io.Copy(out, in)
	ce := out.Close()
	if e != nil {
		return e
	}
	return ce
}
func (s *Service) importFile(ctx context.Context, scope wire.Scope, w wire.Workspace, a map[string]any) (wire.Result, error) {
	if _, ok := a["file"].(string); ok {
		return wire.Result{}, fmt.Errorf("Unsupported attachment reference. ChatGPT must pass an Apps SDK file object with download_url and file_id via openai/fileParams. A bare string file id or URL is not enough.")
	}
	ref := wire.Object(a, "file")
	if ref == nil {
		return wire.Result{}, fmt.Errorf("Unsupported attachment reference. Expected { download_url, file_id, mime_type?, file_name? }.")
	}
	raw, id := TrimJS(wire.String(ref, "download_url", "")), TrimJS(wire.String(ref, "file_id", ""))
	if raw == "" || id == "" {
		return wire.Result{}, wire.Errorf("", "Unsupported attachment reference. Both download_url and file_id are required.")
	}
	lower := strings.ToLower(raw)
	if strings.HasPrefix(lower, "file:") || strings.HasPrefix(lower, "data:") || strings.HasPrefix(lower, "javascript:") {
		return wire.Result{}, wire.Errorf("", "Unsupported attachment reference scheme.")
	}
	dest := TrimJS(wire.String(a, "destination", ""))
	if dest == "" {
		return wire.Result{}, fmt.Errorf("destination is required.")
	}
	abs, rel, e := s.ResolvePath(w, dest, true)
	if e != nil {
		return wire.Result{}, e
	}
	temp, n, contentType, e := s.download(ctx, raw, s.Config.Int("maxImportBytes", 5000000))
	if e != nil {
		return wire.Result{}, e
	}
	defer os.Remove(temp)
	f, e := os.Open(temp)
	if e != nil {
		return wire.Result{}, e
	}
	probe := make([]byte, min(64, n))
	_, e = io.ReadFull(f, probe)
	if e != nil {
		f.Close()
		return wire.Result{}, e
	}
	_, e = f.Seek(0, 0)
	if e != nil {
		f.Close()
		return wire.Result{}, e
	}
	h := sha256.New()
	_, e = io.Copy(h, f)
	f.Close()
	if e != nil {
		return wire.Result{}, e
	}
	hash := hex.EncodeToString(h.Sum(nil))
	detected := detectMime(probe)
	expected := wire.String(a, "expected_sha256", "")
	if expected != "" && !strings.EqualFold(expected, hash) {
		return wire.Result{}, fmt.Errorf("Attachment SHA-256 mismatch for %s.", rel)
	}
	declared := TrimJS(wire.String(ref, "mime_type", ""))
	if declared == "" {
		declared = contentType
	}
	mimeStatus := "unknown"
	if declared != "" && detected != "" {
		mimeStatus = "mismatched"
		if strings.EqualFold(strings.TrimSpace(declared), detected) {
			mimeStatus = "matched"
		}
	}
	_, statErr := os.Stat(abs)
	existed := statErr == nil
	overwrite := wire.Bool(a, "overwrite", false)
	release := s.LockPaths(abs)
	defer release()
	if !overwrite && existed {
		return wire.Result{}, fmt.Errorf("Destination already exists and overwrite=false: %s", rel)
	}
	if _, _, e = s.ResolvePath(w, dest, true); e != nil {
		return wire.Result{}, e
	}
	if e = os.MkdirAll(filepath.Dir(abs), 0777); e != nil {
		return wire.Result{}, e
	}
	if overwrite {
		e = os.Rename(temp, abs)
	} else {
		e = os.Link(temp, abs)
		if e != nil {
			if os.IsExist(e) {
				return wire.Result{}, fmt.Errorf("Destination already exists and overwrite=false: %s", filepath.Base(abs))
			}
			e = copyExclusive(temp, abs)
			if os.IsExist(e) {
				return wire.Result{}, fmt.Errorf("Destination already exists and overwrite=false: %s", filepath.Base(abs))
			}
		}
	}
	if e != nil {
		return wire.Result{}, FileError(e, "rename", abs)
	}
	data := Base(w)
	data["path"] = rel
	data["bytes"] = n
	data["sha256"] = hash
	data["mime_type_status"] = mimeStatus
	data["verified"] = expected != ""
	data["file_id"] = id
	data["overwritten"] = overwrite && existed
	data["declared_mime_type"] = nil
	data["detected_mime_type"] = nil
	data["file_name"] = nil
	if declared != "" {
		data["declared_mime_type"] = declared
	}
	if detected != "" {
		data["detected_mime_type"] = detected
	}
	if name := TrimJS(wire.String(ref, "file_name", "")); name != "" {
		data["file_name"] = name
	}
	dm, tm := declared, detected
	if dm == "" {
		dm = "unknown"
	}
	if tm == "" {
		tm = "unknown"
	}
	text := fmt.Sprintf("# Import File\n\nPath: %s\nBytes: %d\nSHA-256: %s\nDeclared MIME: %s\nDetected MIME: %s\nMIME status: %s\nVerified: %t\nOverwritten: %t", rel, n, hash, dm, tm, mimeStatus, expected != "", overwrite && existed)
	return wire.Success("import_file", "Import Attachment File", data, text), nil
}
