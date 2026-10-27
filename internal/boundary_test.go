package internal

import (
	"go/build"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const module = "github.com/daimoc/jitsi-media-tools/"

// Packages on each side of constitution principle III. internal/session is
// the only glue between them.
var (
	signalingSide = []string{"internal/jitsi", "internal/signaling"}
	mediaSide     = []string{"internal/rtpmedia", "internal/ffmpeg", "internal/compose", "internal/output", "internal/whip"}
)

// TestImportBoundaries checks principles I and III on the source tree:
// signaling and media packages do not import each other, and the CLI only
// uses the public package.
func TestImportBoundaries(t *testing.T) {
	root := ".."
	imports := map[string][]string{} // Package dir (relative to root) -> module imports
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return err
		}
		if name := d.Name(); name == "testdata" || strings.HasPrefix(name, ".") && path != root {
			return filepath.SkipDir
		}
		pkg, err := build.ImportDir(path, build.ImportComment)
		if err != nil {
			return nil // No Go files
		}
		rel, _ := filepath.Rel(root, path)
		for _, imp := range append(pkg.Imports, pkg.TestImports...) {
			if strings.HasPrefix(imp, module) {
				imports[filepath.ToSlash(rel)] = append(imports[filepath.ToSlash(rel)], strings.TrimPrefix(imp, module))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	within := func(pkg string, side []string) bool {
		for _, s := range side {
			if pkg == s || strings.HasPrefix(pkg, s+"/") {
				return true
			}
		}
		return false
	}
	for pkg, imps := range imports {
		for _, imp := range imps {
			switch {
			case within(pkg, signalingSide) && (within(imp, mediaSide) || within(imp, []string{"internal/peer"})):
				t.Errorf("%s (signaling) imports %s (media)", pkg, imp)
			case within(pkg, mediaSide) && within(imp, signalingSide):
				t.Errorf("%s (media) imports %s (signaling)", pkg, imp)
			case strings.HasPrefix(pkg, "cmd/") && imp != "jmt":
				t.Errorf("%s imports %s; the CLI may only use jmt", pkg, imp)
			}
		}
	}
	if len(imports) == 0 {
		t.Fatal("no packages found")
	}
}
