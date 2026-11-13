// Package fs is an MCP server with file tools that can only touch files under
// one root directory.
package fs

import (
	"context"
	"fmt"
	"io"
	iofs "io/fs"
	"os"
	"path"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/roman-ambinder/agentic-framework/internal/buildinfo"
)

const (
	defaultMaxReadBytes  = 256 << 10
	defaultMaxWriteBytes = 1 << 20
	defaultMaxEntries    = 1000
)

// Options limit what the tools do. Zero values mean the defaults: 256 KiB
// for reads, 1 MiB for writes and 1000 directory entries.
type Options struct {
	MaxReadBytes  int64
	MaxWriteBytes int64
	MaxEntries    int
}

func (o Options) withDefaults() Options {
	if o.MaxReadBytes <= 0 {
		o.MaxReadBytes = defaultMaxReadBytes
	}
	if o.MaxWriteBytes <= 0 {
		o.MaxWriteBytes = defaultMaxWriteBytes
	}
	if o.MaxEntries <= 0 {
		o.MaxEntries = defaultMaxEntries
	}
	return o
}

type readIn struct {
	Path string `json:"path" jsonschema:"file path relative to the workspace root"`
}

type readOut struct {
	Content   string `json:"content"`
	Size      int64  `json:"size"`
	Truncated bool   `json:"truncated"`
}

type listIn struct {
	Path string `json:"path,omitempty" jsonschema:"directory relative to the workspace root; default ."`
}

type entry struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Size int64  `json:"size"`
}

type listOut struct {
	Entries   []entry `json:"entries"`
	Truncated bool    `json:"truncated"`
}

type writeIn struct {
	Path    string `json:"path" jsonschema:"file path relative to the workspace root"`
	Content string `json:"content" jsonschema:"full new content of the file"`
}

type writeOut struct {
	BytesWritten int `json:"bytes_written"`
}

// cleanPath returns the cleaned path or an error "<tool> <p>: path escapes the root".
func cleanPath(tool, p string) (string, error) {
	if p == "" {
		p = "."
	}
	c := path.Clean(p)
	if !iofs.ValidPath(c) {
		return "", fmt.Errorf("%s %s: path escapes the root", tool, p)
	}
	return c, nil
}

type server struct {
	root *os.Root
	opts Options
}

// NewServer returns an MCP server named "fs" whose tools work under root.
// The caller keeps ownership of root and closes it.
func NewServer(root *os.Root, opts Options) *mcp.Server {
	s := &server{root: root, opts: opts.withDefaults()}
	srv := mcp.NewServer(&mcp.Implementation{Name: "fs", Version: buildinfo.Get().Version}, nil)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "read_file",
		Description: "Read a UTF-8 text file under the workspace root.",
	}, s.readFile)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_dir",
		Description: "List the entries of a directory under the workspace root.",
	}, s.listDir)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "write_file",
		Description: "Create or overwrite a UTF-8 text file under the workspace root. Parent directories are created.",
	}, s.writeFile)
	return srv
}

func (s *server) readFile(_ context.Context, _ *mcp.CallToolRequest, in readIn) (*mcp.CallToolResult, readOut, error) {
	p, err := cleanPath("read_file", in.Path)
	if err != nil {
		return nil, readOut{}, err
	}
	info, err := s.root.Stat(p)
	if err != nil {
		return nil, readOut{}, fmt.Errorf("read_file %s: %w", p, err)
	}
	if info.IsDir() {
		return nil, readOut{}, fmt.Errorf("read_file %s: is a directory", p)
	}
	f, err := s.root.Open(p)
	if err != nil {
		return nil, readOut{}, fmt.Errorf("read_file %s: %w", p, err)
	}
	defer f.Close()
	max := s.opts.MaxReadBytes
	data, err := io.ReadAll(io.LimitReader(f, max))
	if err != nil {
		return nil, readOut{}, fmt.Errorf("read_file %s: %w", p, err)
	}
	truncated := info.Size() > max
	if truncated {
		// The cut may have split a character; drop up to 3 trailing bytes.
		for i := 0; i < 3 && len(data) > 0 && !utf8.Valid(data); i++ {
			data = data[:len(data)-1]
		}
	}
	if !utf8.Valid(data) {
		return nil, readOut{}, fmt.Errorf("read_file %s: not a UTF-8 text file", p)
	}
	return nil, readOut{Content: string(data), Size: info.Size(), Truncated: truncated}, nil
}

func entryType(m iofs.FileMode) string {
	switch {
	case m.IsDir():
		return "dir"
	case m&iofs.ModeSymlink != 0:
		return "symlink"
	case m.IsRegular():
		return "file"
	default:
		return "other"
	}
}

func (s *server) listDir(_ context.Context, _ *mcp.CallToolRequest, in listIn) (*mcp.CallToolResult, listOut, error) {
	p, err := cleanPath("list_dir", in.Path)
	if err != nil {
		return nil, listOut{}, err
	}
	dirents, err := iofs.ReadDir(s.root.FS(), p)
	if err != nil {
		return nil, listOut{}, fmt.Errorf("list_dir %s: %w", p, err)
	}
	out := listOut{Entries: []entry{}}
	for _, d := range dirents {
		if len(out.Entries) >= s.opts.MaxEntries {
			out.Truncated = true
			break
		}
		e := entry{Name: d.Name(), Type: entryType(d.Type())}
		if e.Type == "file" {
			info, err := d.Info()
			if err != nil {
				return nil, listOut{}, fmt.Errorf("list_dir %s: %w", p, err)
			}
			e.Size = info.Size()
		}
		out.Entries = append(out.Entries, e)
	}
	return nil, out, nil
}

func (s *server) writeFile(_ context.Context, _ *mcp.CallToolRequest, in writeIn) (*mcp.CallToolResult, writeOut, error) {
	p, err := cleanPath("write_file", in.Path)
	if err != nil {
		return nil, writeOut{}, err
	}
	if p == "." {
		return nil, writeOut{}, fmt.Errorf("write_file: path is required")
	}
	if int64(len(in.Content)) > s.opts.MaxWriteBytes {
		return nil, writeOut{}, fmt.Errorf("write_file %s: content is %d bytes, limit is %d", p, len(in.Content), s.opts.MaxWriteBytes)
	}
	if dir := path.Dir(p); dir != "." {
		if err := s.root.MkdirAll(dir, 0o755); err != nil {
			return nil, writeOut{}, fmt.Errorf("write_file %s: %w", p, err)
		}
	}
	if err := s.root.WriteFile(p, []byte(in.Content), 0o644); err != nil {
		return nil, writeOut{}, fmt.Errorf("write_file %s: %w", p, err)
	}
	return nil, writeOut{BytesWritten: len(in.Content)}, nil
}
