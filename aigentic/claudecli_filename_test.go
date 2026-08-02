package aigentic

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// safeName must give a media file a recognizable extension when the caller's path lacks one, so
// the agentic CLI reads it for what it is instead of stalling over opaque binary.
func TestSafeNameDerivesExtensionFromMediaType(t *testing.T) {
	cases := []struct {
		name      string
		path      string
		mediaType string
		want      string
	}{
		{"jpeg without extension", "image on page 6", "image/jpeg", "image on page 6.jpg"},
		{"png without extension", "screenshot", "image/png", "screenshot.png"},
		{"pdf without extension", "scan", "application/pdf", "scan.pdf"},
		{"media type with params", "note", "text/plain; charset=utf-8", "note.txt"},
		{"correct extension kept", "photo.jpg", "image/jpeg", "photo.jpg"},
		{"alternate valid extension kept", "photo.jpeg", "image/jpeg", "photo.jpeg"},
		{"wrong extension gets a fitting one appended", "photo.bin", "image/png", "photo.bin.png"},
		{"empty path plus media type", "", "image/png", "_.png"},
		{"unknown media type left untouched", "blob", "application/x-not-a-real-type", "blob"},
		{"text with no media type left untouched", "readme", "", "readme"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := safeName(c.path, c.mediaType, map[string]int{})
			if got != c.want {
				t.Fatalf("safeName(%q, %q) = %q, want %q", c.path, c.mediaType, got, c.want)
			}
		})
	}
}

// De-duplication still works after an extension is derived: two extensionless JPEGs with the same
// path must not collide on disk.
func TestSafeNameDedupWithDerivedExtension(t *testing.T) {
	seen := map[string]int{}
	first := safeName("image on page 6", "image/jpeg", seen)
	second := safeName("image on page 6", "image/jpeg", seen)
	if first != "image on page 6.jpg" {
		t.Fatalf("first = %q, want %q", first, "image on page 6.jpg")
	}
	if second == first {
		t.Fatalf("second name %q collides with first", second)
	}
	if filepath.Ext(second) != ".jpg" {
		t.Fatalf("second = %q, want a .jpg extension preserved on the dedup suffix", second)
	}
}

// End to end through materializeCLIFiles: an extensionless JPEG lands on disk under a .jpg name and
// its provenance keeps the caller's original path.
func TestMaterializeCLIFilesGivesImageAnExtension(t *testing.T) {
	req := Request{Inline: []InlineFile{{
		Path:      "image on page 6",
		MediaType: "image/jpeg",
		Content:   base64.StdEncoding.EncodeToString([]byte{0xff, 0xd8, 0xff, 0xe0}),
	}}}
	dir, listing, items, err := materializeCLIFiles(req)
	if err != nil {
		t.Fatalf("materializeCLIFiles: %v", err)
	}
	defer os.RemoveAll(dir)

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	if len(names) != 1 || names[0] != "image on page 6.jpg" {
		t.Fatalf("written files = %v, want [image on page 6.jpg]", names)
	}
	if want := "- image on page 6.jpg\n"; listing != want {
		t.Fatalf("listing = %q, want %q", listing, want)
	}
	if len(items) != 1 || items[0].Path != "image on page 6" || items[0].Bytes != 4 {
		t.Fatalf("provenance = %+v, want the caller's original path with 4 bytes", items)
	}
}
