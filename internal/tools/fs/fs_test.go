package fs

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func newRoot(t *testing.T) (*os.Root, string) {
	t.Helper()
	dir := t.TempDir()
	r, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r, dir
}

func connect(t *testing.T, root *os.Root, opts Options) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	ct, st := mcp.NewInMemoryTransports()
	ss, err := NewServer(root, opts).Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func call(t *testing.T, cs *mcp.ClientSession, name, args string) (string, bool) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: json.RawMessage(args)})
	if err != nil {
		t.Fatal(err)
	}
	var text string
	if len(res.Content) > 0 {
		if tc, ok := res.Content[0].(*mcp.TextContent); ok {
			text = tc.Text
		}
	}
	return text, res.IsError
}

func callOK[T any](t *testing.T, cs *mcp.ClientSession, name, args string) T {
	t.Helper()
	text, isErr := call(t, cs, name, args)
	if isErr {
		t.Fatalf("%s %s: unexpected error: %s", name, args, text)
	}
	var out T
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func callErr(t *testing.T, cs *mcp.ClientSession, name, args, want string) {
	t.Helper()
	text, isErr := call(t, cs, name, args)
	if !isErr {
		t.Fatalf("%s %s: want error, got %s", name, args, text)
	}
	if !strings.Contains(text, want) {
		t.Fatalf("%s %s: error %q does not contain %q", name, args, text, want)
	}
}

func write(t *testing.T, dir, name string, data []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestListsThreeTools(t *testing.T) {
	r, _ := newRoot(t)
	cs := connect(t, r, Options{})
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range res.Tools {
		names = append(names, tl.Name)
	}
	slices.Sort(names)
	if want := []string{"list_dir", "read_file", "write_file"}; !slices.Equal(names, want) {
		t.Fatalf("tools = %v, want %v", names, want)
	}
}

func TestReadFile(t *testing.T) {
	r, dir := newRoot(t)
	write(t, dir, "a.txt", []byte("hello"))
	cs := connect(t, r, Options{})
	got := callOK[readOut](t, cs, "read_file", `{"path":"a.txt"}`)
	if got != (readOut{Content: "hello", Size: 5}) {
		t.Fatalf("got %+v", got)
	}
}

func TestReadFileTruncates(t *testing.T) {
	r, dir := newRoot(t)
	write(t, dir, "a.txt", []byte("hello"))
	cs := connect(t, r, Options{MaxReadBytes: 4})
	got := callOK[readOut](t, cs, "read_file", `{"path":"a.txt"}`)
	if got != (readOut{Content: "hell", Size: 5, Truncated: true}) {
		t.Fatalf("got %+v", got)
	}
}

func TestReadFileUTF8CutNotBinary(t *testing.T) {
	r, dir := newRoot(t)
	write(t, dir, "a.txt", []byte("aé"))
	cs := connect(t, r, Options{MaxReadBytes: 2})
	got := callOK[readOut](t, cs, "read_file", `{"path":"a.txt"}`)
	if got != (readOut{Content: "a", Size: 3, Truncated: true}) {
		t.Fatalf("got %+v", got)
	}
}

func TestReadFileBinary(t *testing.T) {
	r, dir := newRoot(t)
	write(t, dir, "b.bin", []byte{0xff, 0xfe})
	cs := connect(t, r, Options{})
	callErr(t, cs, "read_file", `{"path":"b.bin"}`, "not a UTF-8 text file")
}

func TestReadFileDirectory(t *testing.T) {
	r, dir := newRoot(t)
	if err := os.Mkdir(filepath.Join(dir, "d"), 0o755); err != nil {
		t.Fatal(err)
	}
	cs := connect(t, r, Options{})
	callErr(t, cs, "read_file", `{"path":"d"}`, "is a directory")
}

func TestReadFileMissing(t *testing.T) {
	r, _ := newRoot(t)
	cs := connect(t, r, Options{})
	callErr(t, cs, "read_file", `{"path":"nope.txt"}`, "no such file")
}

func TestEscapesAreRejected(t *testing.T) {
	parent := t.TempDir()
	rootDir := filepath.Join(parent, "root")
	if err := os.Mkdir(rootDir, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(parent, "outside.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(rootDir, "link")); err != nil {
		t.Fatal(err)
	}
	r, err := os.OpenRoot(rootDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	cs := connect(t, r, Options{})

	tests := []struct{ name, tool, args string }{
		{"read dotdot", "read_file", `{"path":"../outside.txt"}`},
		{"read absolute", "read_file", `{"path":"/etc/passwd"}`},
		{"read symlink", "read_file", `{"path":"link"}`},
		{"list dotdot", "list_dir", `{"path":".."}`},
		{"list absolute", "list_dir", `{"path":"/etc"}`},
		{"write dotdot", "write_file", `{"path":"../outside.txt","content":"x"}`},
		{"write absolute", "write_file", `{"path":"/tmp/adlc-fs-escape.txt","content":"x"}`},
		{"write symlink", "write_file", `{"path":"link","content":"x"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			callErr(t, cs, tt.tool, tt.args, "escapes")
		})
	}
	got, err := os.ReadFile(outside)
	if err != nil || string(got) != "secret" {
		t.Fatalf("outside file changed: %q, %v", got, err)
	}
	if _, err := os.Stat("/tmp/adlc-fs-escape.txt"); err == nil {
		t.Fatal("absolute write escaped")
	}
}

func TestListDir(t *testing.T) {
	r, dir := newRoot(t)
	write(t, dir, "b.txt", nil)
	write(t, dir, "a.txt", []byte("abc"))
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	cs := connect(t, r, Options{})
	got := callOK[listOut](t, cs, "list_dir", `{"path":"."}`)
	want := []entry{{"a.txt", "file", 3}, {"b.txt", "file", 0}, {"sub", "dir", 0}}
	if !slices.Equal(got.Entries, want) || got.Truncated {
		t.Fatalf("got %+v", got)
	}
}

func TestListDirDefaultPath(t *testing.T) {
	r, dir := newRoot(t)
	write(t, dir, "a.txt", nil)
	cs := connect(t, r, Options{})
	got := callOK[listOut](t, cs, "list_dir", `{}`)
	if len(got.Entries) != 1 || got.Entries[0].Name != "a.txt" {
		t.Fatalf("got %+v", got)
	}
}

func TestListDirEmptyIsArray(t *testing.T) {
	r, _ := newRoot(t)
	cs := connect(t, r, Options{})
	text, isErr := call(t, cs, "list_dir", `{}`)
	if isErr || !strings.Contains(text, `"entries":[]`) {
		t.Fatalf("got %q (error %v)", text, isErr)
	}
}

func TestListDirTruncates(t *testing.T) {
	r, dir := newRoot(t)
	for _, n := range []string{"a", "b", "c"} {
		write(t, dir, n, nil)
	}
	cs := connect(t, r, Options{MaxEntries: 2})
	got := callOK[listOut](t, cs, "list_dir", `{}`)
	if len(got.Entries) != 2 || !got.Truncated {
		t.Fatalf("got %+v", got)
	}
}

func TestWriteFileCreatesParents(t *testing.T) {
	r, dir := newRoot(t)
	cs := connect(t, r, Options{})
	got := callOK[writeOut](t, cs, "write_file", `{"path":"x/y/z.txt","content":"héllo"}`)
	if got.BytesWritten != 6 {
		t.Fatalf("BytesWritten = %d, want 6", got.BytesWritten)
	}
	data, err := os.ReadFile(filepath.Join(dir, "x", "y", "z.txt"))
	if err != nil || string(data) != "héllo" {
		t.Fatalf("file = %q, %v", data, err)
	}
}

func TestWriteFileOverwrites(t *testing.T) {
	r, dir := newRoot(t)
	write(t, dir, "a.txt", []byte("old content"))
	cs := connect(t, r, Options{})
	callOK[writeOut](t, cs, "write_file", `{"path":"a.txt","content":"new"}`)
	data, _ := os.ReadFile(filepath.Join(dir, "a.txt"))
	if string(data) != "new" {
		t.Fatalf("file = %q", data)
	}
}

func TestWriteFileTooLarge(t *testing.T) {
	r, dir := newRoot(t)
	cs := connect(t, r, Options{MaxWriteBytes: 3})
	callErr(t, cs, "write_file", `{"path":"a.txt","content":"abcd"}`, "limit is 3")
	if _, err := os.Stat(filepath.Join(dir, "a.txt")); err == nil {
		t.Fatal("file was created")
	}
}

func TestWriteFileRequiresPath(t *testing.T) {
	r, _ := newRoot(t)
	cs := connect(t, r, Options{})
	callErr(t, cs, "write_file", `{"path":"","content":"x"}`, "path is required")
}
