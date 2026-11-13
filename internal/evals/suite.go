// Package evals loads eval suites from disk, prepares git workspaces for
// seeded bugs and parses the output of their hidden tests. The on-disk format
// is described in docs/impl/tasks/E.3-fixture-loader.md and evals/suites/README.md.
package evals

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	yaml "github.com/goccy/go-yaml"
)

// Difficulty of a seeded bug.
type Difficulty string

const (
	// Easy bugs have their cause in one function.
	Easy Difficulty = "easy"
	// Medium bugs have their cause in one file, found through 2-3 packages.
	Medium Difficulty = "medium"
	// Hard bugs need an invariant that spans two or more files or packages.
	Hard Difficulty = "hard"
)

// Meta is the content of a bug's meta.yaml.
type Meta struct {
	ID          string        `yaml:"id"`
	Difficulty  Difficulty    `yaml:"difficulty"`
	Title       string        `yaml:"title"`
	Fixture     string        `yaml:"fixture"`
	TestPackage string        `yaml:"test_package"`
	HiddenTests []string      `yaml:"hidden_tests"`
	Timeout     time.Duration `yaml:"timeout"`
	FixFiles    []string      `yaml:"fix_files"`
}

// Bug is one seeded bug with its files loaded into memory.
type Bug struct {
	Meta
	Dir        string // absolute path of the bug directory
	Issue      string // issue.md content
	BugPatch   []byte
	FixPatch   []byte
	HiddenTest []byte // hidden_test.go content
}

// Fixture is a clean Go module that bugs are applied to.
type Fixture struct {
	Name string
	Dir  string // absolute path
}

// Suite is a loaded eval suite.
type Suite struct {
	Name     string             // base name of the suite directory, e.g. "go-bugs"
	Dir      string             // absolute path
	Fixtures map[string]Fixture // by name; only fixtures some bug uses
	Bugs     []Bug              // sorted by ID
}

// ErrInvalidSuite wraps every validation problem.
var ErrInvalidSuite = errors.New("evals: invalid suite")

var (
	idPattern         = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	hiddenNamePattern = regexp.MustCompile(`^TestHidden[A-Za-z0-9_]*$`)
)

const (
	minTimeout = time.Second
	maxTimeout = 10 * time.Minute
)

// LoadSuite loads and validates the suite in dir. Fixtures are looked up in
// filepath.Join(dir, "..", "..", "fixtures"), i.e. evals/fixtures for
// evals/suites/<name>.
func LoadSuite(dir string) (*Suite, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("evals: suite dir: %w", err)
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return nil, fmt.Errorf("evals: read suite: %w", err)
	}
	fixturesDir := filepath.Join(abs, "..", "..", "fixtures")
	suite := &Suite{Name: filepath.Base(abs), Dir: abs, Fixtures: map[string]Fixture{}}
	var problems []error
	bugDirs := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		bugDirs++
		bug, errs := loadBug(abs, e.Name(), fixturesDir)
		problems = append(problems, errs...)
		if len(errs) == 0 {
			suite.Bugs = append(suite.Bugs, bug)
			suite.Fixtures[bug.Fixture] = Fixture{Name: bug.Fixture, Dir: filepath.Join(fixturesDir, bug.Fixture)}
		}
	}
	if bugDirs == 0 {
		problems = append(problems, errors.New("evals: suite has no bugs"))
	}
	if len(problems) > 0 {
		return nil, fmt.Errorf("%w:\n%w", ErrInvalidSuite, errors.Join(problems...))
	}
	slices.SortFunc(suite.Bugs, func(a, b Bug) int { return strings.Compare(a.ID, b.ID) })
	return suite, nil
}

// Bug returns the bug with the given id, or false.
func (s *Suite) Bug(id string) (Bug, bool) {
	for _, b := range s.Bugs {
		if b.ID == id {
			return b, true
		}
	}
	return Bug{}, false
}

// checker collects the validation problems of one bug directory.
type checker struct {
	name string // bug directory name
	errs []error
}

func (c *checker) addf(format string, args ...any) {
	c.errs = append(c.errs, fmt.Errorf("evals: %s: %s", c.name, fmt.Sprintf(format, args...)))
}

// loadBug reads and checks one bug directory. It returns the bug and every
// problem found; the bug is only usable when there are no problems.
func loadBug(suiteDir, name, fixturesDir string) (Bug, []error) {
	c := &checker{name: name}
	bug := Bug{Dir: filepath.Join(suiteDir, name)}
	c.readFiles(&bug)
	if err := readMeta(bug.Dir, &bug.Meta); err != nil {
		c.addf("meta.yaml: %v", err)
		return bug, c.errs
	}
	c.checkMeta(bug.Meta)
	c.checkHiddenTests(bug.HiddenTests, bug.HiddenTest)
	if fixDir, ok := c.checkFixture(bug.Fixture, fixturesDir); ok {
		c.checkTestPackage(bug.TestPackage, fixDir, bug.Fixture)
		c.checkFixFiles(bug.FixFiles, fixDir, bug.Fixture)
	}
	return bug, c.errs
}

func readMeta(bugDir string, meta *Meta) error {
	data, err := os.ReadFile(filepath.Join(bugDir, "meta.yaml"))
	if err != nil {
		return err
	}
	return yaml.UnmarshalWithOptions(data, meta, yaml.DisallowUnknownField())
}

// readFiles loads the four files that every bug directory must contain.
func (c *checker) readFiles(bug *Bug) {
	bug.Issue = string(c.readRequired(bug.Dir, "issue.md"))
	bug.BugPatch = c.readRequired(bug.Dir, "bug.patch")
	bug.FixPatch = c.readRequired(bug.Dir, "fix.patch")
	bug.HiddenTest = c.readRequired(bug.Dir, "hidden_test.go")
}

func (c *checker) readRequired(bugDir, file string) []byte {
	data, err := os.ReadFile(filepath.Join(bugDir, file))
	if err != nil || len(bytes.TrimSpace(data)) == 0 {
		c.addf("%s is missing or empty", file)
		return nil
	}
	return data
}

// checkMeta checks the fields of meta.yaml that need no other file.
func (c *checker) checkMeta(m Meta) {
	if m.ID != c.name {
		c.addf("id %q does not match directory name", m.ID)
	}
	if !idPattern.MatchString(m.ID) {
		c.addf("id %q must be lowercase letters, digits and dashes", m.ID)
	}
	switch m.Difficulty {
	case Easy, Medium, Hard:
	default:
		c.addf("difficulty %q must be easy, medium or hard", m.Difficulty)
	}
	if strings.TrimSpace(m.Title) == "" {
		c.addf("title is empty")
	}
	if m.Timeout < minTimeout || m.Timeout > maxTimeout {
		c.addf("timeout must be between 1s and 10m")
	}
	if len(m.HiddenTests) == 0 {
		c.addf("hidden_tests is empty")
	}
	if len(m.FixFiles) == 0 {
		c.addf("fix_files is empty")
	}
}

// checkHiddenTests checks each hidden test name, and that hidden_test.go
// declares it (when the file is present).
func (c *checker) checkHiddenTests(names []string, src []byte) {
	for _, n := range names {
		if !hiddenNamePattern.MatchString(n) {
			c.addf("hidden test %q must match ^TestHidden[A-Za-z0-9_]*$", n)
			continue
		}
		if len(src) == 0 {
			continue // already reported as a missing file
		}
		decl := regexp.MustCompile(`(?m)^func ` + regexp.QuoteMeta(n) + `\(t \*testing\.T\)`)
		if !decl.Match(src) {
			c.addf("hidden test %q not found in hidden_test.go", n)
		}
	}
}

// checkFixture reports whether the fixture exists and returns its directory.
func (c *checker) checkFixture(name, fixturesDir string) (string, bool) {
	dir := filepath.Join(fixturesDir, name)
	if name == "" || !filepath.IsLocal(name) || !isRegularFile(filepath.Join(dir, "go.mod")) {
		c.addf("fixture %q not found (looked in %s)", name, dir)
		return "", false
	}
	return dir, true
}

func (c *checker) checkTestPackage(pkg, fixDir, fixture string) {
	if !filepath.IsLocal(pkg) || !isDir(filepath.Join(fixDir, pkg)) {
		c.addf("test_package %q not found in fixture %q", pkg, fixture)
	}
}

func (c *checker) checkFixFiles(files []string, fixDir, fixture string) {
	for _, f := range files {
		if !filepath.IsLocal(f) || !isRegularFile(filepath.Join(fixDir, f)) {
			c.addf("fix file %q not found in fixture %q", f, fixture)
		}
	}
}

// isRegularFile reports whether p is a regular file (a symlink is not).
func isRegularFile(p string) bool {
	fi, err := os.Lstat(p)
	return err == nil && fi.Mode().IsRegular()
}

// isDir reports whether p is a directory (a symlink is not).
func isDir(p string) bool {
	fi, err := os.Lstat(p)
	return err == nil && fi.IsDir()
}
