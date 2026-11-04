package files

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/lyeith/codexpro/go/internal/wire"
)

func TestPersonalFilePinsAndImagePixels(t *testing.T) {
	s, w, scope := fixture(t)
	scope.Identity = wire.Identity{Source: "named", PrincipalType: "person", Role: "tester"}
	outside := t.TempDir()
	writeFixture(t, filepath.Join(outside, "private"), "other fixture")
	if err := os.Mkdir(filepath.Join(w.Root, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(w.Root, "nested", ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(w.Root, "nested", "private"), "nested fixture")
	writeFixture(t, filepath.Join(w.Root, ".env"), "env fixture")
	if err := os.Link(filepath.Join(outside, "private"), filepath.Join(w.Root, "alias")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(w.Root, "link")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(w.Root, "pipe"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"alias", "link/private", "nested/private", ".env", "pipe", filepath.Join(outside, "private")} {
		if _, _, err := s.ReadProjectFile(w, p, 4096, false); err == nil || strings.Contains(err.Error(), outside) {
			t.Fatal("read admitted private entry or disclosed host path")
		}
	}
	current := filepath.Join(w.Root, "current")
	if err := os.Mkdir(current, 0700); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(current, "private"), "first fixture")
	f, _, err := s.openProjectRead(w, "current/private")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := os.Rename(current, current+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, current); err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 13)
	if _, err := f.Read(b); err != nil || string(b) != "first fixture" {
		t.Fatal("open handle lost identity")
	}
	if _, _, err := s.ReadProjectFile(w, "current/private", 4096, false); err == nil {
		t.Fatal("path replacement escaped project")
	}
	tree := call(t, s, scope, "tree", nil).StructuredContent["text"].(string)
	for _, name := range []string{"alias", "link/", "nested", "pipe", ".env"} {
		if strings.Contains(tree, name) {
			t.Fatal("tree exposed blocked entry", name)
		}
	}

	img := image.NewPaletted(image.Rect(0, 0, 2, 2), color.Palette{color.Black, color.White, color.RGBA{1, 2, 3, 255}})
	img.SetColorIndex(1, 0, 1)
	var raw bytes.Buffer
	if err := png.Encode(&raw, img); err != nil {
		t.Fatal(err)
	}
	suffix := []byte("private trailer fixture")
	raw.Write(suffix)
	if err := os.WriteFile(filepath.Join(w.Root, "picture"), raw.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	r := call(t, s, scope, "view_image", map[string]any{"path": "picture"})
	out, err := base64.StdEncoding.DecodeString(r.Content[1]["data"].(string))
	if err != nil || bytes.Contains(out, suffix) || bytes.Contains(out, []byte("PLTE")) {
		t.Fatal("image contained source trailer or original palette")
	}
	decoded, err := png.Decode(bytes.NewReader(out))
	if err != nil || decoded.Bounds() != img.Bounds() {
		t.Fatal("pixel projection lost image dimensions")
	}
	for x := 0; x < 2; x++ {
		for y := 0; y < 2; y++ {
			if color.RGBAModel.Convert(decoded.At(x, y)) != color.RGBAModel.Convert(img.At(x, y)) {
				t.Fatal("projected image changed pixel")
			}
		}
	}
	for _, bad := range [][]byte{raw.Bytes()[:24], []byte("RIFFxxxxWEBPVP8X")} {
		if _, err := projectImage(bad, 10000); err == nil {
			t.Fatal("invalid or unsupported image admitted")
		}
	}
	if _, err := projectImage(raw.Bytes(), 16); err == nil {
		t.Fatal("image output budget ignored")
	}
}
