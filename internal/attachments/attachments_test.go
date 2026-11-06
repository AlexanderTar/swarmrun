package attachments

import (
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/AlexanderTar/swarmrun/internal/items"
	"github.com/AlexanderTar/swarmrun/internal/kinds"
)

func png(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/one.png")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func in(name string, b []byte) Input {
	return Input{Name: name, Data: base64.StdEncoding.EncodeToString(b)}
}

func badRequest(t *testing.T, err error, want string) {
	t.Helper()
	var ie *items.Error
	if !errors.As(err, &ie) || ie.Code != items.CodeBadRequest || ie.Message != want {
		t.Fatalf("err = %v, want bad_request %q", err, want)
	}
}

func TestDecodeAcceptsPNGAndSniffsExtension(t *testing.T) {
	files, err := Decode([]Input{in("Login bug.JPG", png(t))})
	if err != nil || len(files) != 1 || files[0].Ext != ".png" || files[0].Name != "Login bug.JPG" {
		t.Fatalf("Decode = %+v, %v", files, err)
	}
}

func TestDecodeRefusals(t *testing.T) {
	var eleven []Input
	for range 11 {
		eleven = append(eleven, in("a.png", png(t)))
	}
	_, err := Decode(eleven)
	badRequest(t, err, "At most 10 attachments.")

	_, err = Decode([]Input{{Name: "x.pdf", Data: "%%%"}})
	badRequest(t, err, `Attachment "x.pdf" is not valid base64.`)

	big := append(png(t), make([]byte, MaxBytes)...)
	_, err = Decode([]Input{in("big.png", big)})
	badRequest(t, err, `Attachment "big.png" is larger than 10 MB.`)

	_, err = Decode([]Input{in("big.pdf", make([]byte, MaxBytes+1))})
	badRequest(t, err, `Attachment "big.pdf" is larger than 10 MB.`)
}

func TestDecodeAcceptsAnyFileAndKeepsTheSanitizedNameExtension(t *testing.T) {
	for name, want := range map[string]string{
		"Spec.PDF":           ".pdf",
		"notes.md":           ".md",
		"backup.tar.gz":      ".gz",
		"Makefile":           "",
		"weird.c++":          ".c",
		"x.abcdefghijklmnop": ".abcdefghij",
		"text-posing-as.png": ".png",
		"trailing-dot.":      "",
		"archive.zip":        ".zip",
	} {
		files, err := Decode([]Input{in(name, []byte("plain bytes, not an image"))})
		if err != nil || len(files) != 1 || files[0].Ext != want || files[0].Name != name {
			t.Errorf("Decode(%q) = %+v, %v, want ext %q", name, files, err, want)
		}
	}
}

func TestDecodeImageExtensionWinsOverTheName(t *testing.T) {
	files, err := Decode([]Input{in("photo.pdf", png(t))})
	if err != nil || files[0].Ext != ".png" {
		t.Fatalf("Decode = %+v, %v, want sniffed .png", files, err)
	}
}

func TestSafeName(t *testing.T) {
	for in, want := range map[string]string{
		"Login bug.png":         "login-bug.png",
		"../../etc/passwd":      "etc-passwd",
		"   ":                   "attachment",
		strings.Repeat("a", 90): strings.Repeat("a", 60),
	} {
		if got := safeName(in); got != want {
			t.Errorf("safeName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSaveWritesPrivateNumberedFiles(t *testing.T) {
	home := t.TempDir()
	files, _ := Decode([]Input{in("Login bug.png", png(t)), in("second.png", png(t))})
	saved, err := Save(home, "SPIKE-12", files)
	if err != nil || len(saved.Files) != 2 {
		t.Fatalf("Save = %+v, %v", saved, err)
	}
	if saved.Files[0].Name != "Login bug.png" || filepath.Base(saved.Files[0].Path) != "01-login-bug.png" {
		t.Fatalf("saved[0] = %+v", saved.Files[0])
	}
	if filepath.Dir(saved.Files[0].Path) != saved.Dir || !strings.HasPrefix(filepath.Base(saved.Dir), "batch-") {
		t.Fatalf("batch dir = %s", saved.Dir)
	}
	if rel, err := filepath.Rel(Dir(home, "SPIKE-12"), saved.Dir); err != nil || strings.HasPrefix(rel, "..") {
		t.Fatalf("batch is not under the item dir: %s", saved.Dir)
	}
	if fi, _ := os.Stat(Dir(home, "SPIKE-12")); fi.Mode().Perm() != 0o700 {
		t.Errorf("item dir mode = %v", fi.Mode().Perm())
	}
	if fi, _ := os.Stat(saved.Dir); fi.Mode().Perm() != 0o700 {
		t.Errorf("batch dir mode = %v", fi.Mode().Perm())
	}
	if fi, _ := os.Stat(saved.Files[1].Path); fi.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %v", fi.Mode().Perm())
	}
}

func TestSaveNamesFilesByTheirSanitizedExtension(t *testing.T) {
	home := t.TempDir()
	files, err := Decode([]Input{in("Spec Sheet.PDF", []byte("%PDF-1.4")), in("README", []byte("hi")), in("notes.md", []byte("# hi")), in("shot.png", png(t))})
	if err != nil {
		t.Fatal(err)
	}
	saved, err := Save(home, "SPIKE-7", files)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"01-spec-sheet.pdf", "02-readme", "03-notes.md", "04-shot.png"} {
		if got := filepath.Base(saved.Files[i].Path); got != want {
			t.Errorf("saved[%d] = %q, want %q", i, got, want)
		}
	}
}

func TestSecondBatchFailureKeepsFirstBatch(t *testing.T) {
	home := t.TempDir()
	files, err := Decode([]Input{in("a.png", png(t)), in("b.png", png(t))})
	if err != nil {
		t.Fatal(err)
	}
	first, err := Save(home, "SPIKE-1", files[:1])
	if err != nil {
		t.Fatal(err)
	}
	orig := WriteFile
	n := 0
	WriteFile = func(name string, data []byte, perm os.FileMode) error {
		n++
		if n == 2 {
			return errors.New("disk full")
		}
		return orig(name, data, perm)
	}
	t.Cleanup(func() { WriteFile = orig })
	if _, err := Save(home, "SPIKE-1", files); err == nil {
		t.Fatal("second Save succeeded")
	}
	body, err := os.ReadFile(first.Files[0].Path)
	if err != nil {
		t.Fatalf("first batch was removed: %v", err)
	}
	if string(body) != string(files[0].Body) {
		t.Fatal("first batch contents changed")
	}
	entries, err := os.ReadDir(Dir(home, "SPIKE-1"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("item dir = %v, want only the first batch", entries)
	}
}

func TestSameNamesInTwoBatchesBothSurvive(t *testing.T) {
	home := t.TempDir()
	files, err := Decode([]Input{in("notes.md", []byte("one")), in("notes.md", []byte("two"))})
	if err != nil {
		t.Fatal(err)
	}
	first, err := Save(home, "SPIKE-1", files[:1])
	if err != nil {
		t.Fatal(err)
	}
	second, err := Save(home, "SPIKE-1", files[1:])
	if err != nil {
		t.Fatal(err)
	}
	if first.Files[0].Path == second.Files[0].Path {
		t.Fatalf("both batches wrote %s", first.Files[0].Path)
	}
	got1, err := os.ReadFile(first.Files[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	got2, err := os.ReadFile(second.Files[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got1) != "one" || string(got2) != "two" {
		t.Fatalf("contents = %q %q", got1, got2)
	}
}

func TestBriefSectionCutsLongNames(t *testing.T) {
	long := strings.Repeat("é", 81)
	path := "/h/attachments/SPIKE-1/batch-x/01-file.png"
	got := BriefSection([]Saved{{Path: path, Name: long}})
	if !strings.Contains(got, path) {
		t.Fatalf("brief dropped the path: %s", got)
	}
	if strings.Contains(got, long) {
		t.Fatalf("display name was not cut: %s", got)
	}
	if !strings.Contains(got, "("+strings.Repeat("é", 80)+")") {
		t.Fatalf("brief = %s, want the name cut to 80 runes", got)
	}
}

func TestBriefSections(t *testing.T) {
	got := BriefSection([]Saved{{Path: "/h/attachments/SPIKE-12/01-login-bug.png", Name: "Login bug.png"}})
	want := "## Attachments\n\nThe user attached these files to the request. Open each one with your file or image tool before you plan, and pass the paths that matter to children in their briefs.\n\n- /h/attachments/SPIKE-12/01-login-bug.png (Login bug.png)"
	if got != want {
		t.Errorf("BriefSection =\n%s\nwant\n%s", got, want)
	}
	got = FailureSection(3, errors.New("disk full"))
	want = "## Attachments\n\nThe user attached 3 files, but Swarm could not save them (disk full). Ask the user to share them another way."
	if got != want {
		t.Errorf("FailureSection = %q", got)
	}
}

// The package doc's probe table must list every agent kind, so adding a kind
// without probing whether it can open an attached image fails here.
func TestImageProbeTableCoversEveryAgentKind(t *testing.T) {
	src, err := os.ReadFile("attachments.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range kinds.AgentKinds {
		if !regexp.MustCompile(`(?m)^//\s+` + regexp.QuoteMeta(string(k)) + `\s+\S`).Match(src) {
			t.Errorf("attachments.go's image probe table has no row for %s", k)
		}
	}
}
