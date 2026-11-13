package evals

import (
	"fmt"
	"io"
	"slices"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
)

// AgentPair is a base agent id compared with a new agent id.
type AgentPair struct {
	Base string `json:"base"`
	New  string `json:"new"`
}

// ReportInfo describes one input report.
type ReportInfo struct {
	Path    string `json:"path"` // set by the caller; Compare leaves it empty
	Suite   string `json:"suite"`
	Results int    `json:"results"`
	Repeat  int    `json:"repeat"` // Report.Repeat, 0 read as 1
}

// CompareRow is one difficulty, or "all", of one pair.
type CompareRow struct {
	Difficulty string      `json:"difficulty"` // "easy", "medium", "hard" or "all"
	Bugs       int         `json:"bugs"`
	Base       AgentBucket `json:"base"`
	New        AgentBucket `json:"new"`
}

// BugChange is a bug whose status differs between the reports.
type BugChange struct {
	BugID      string      `json:"bug_id"`
	Difficulty Difficulty  `json:"difficulty"`
	Base       BugAttempts `json:"base"`
	New        BugAttempts `json:"new"`
	BaseStatus string      `json:"base_status"`
	NewStatus  string      `json:"new_status"`
}

// PairComparison compares one pair on the bugs both reports have for it.
type PairComparison struct {
	AgentPair
	K            int          `json:"k"` // pass@k column; 0 = pass@1 only
	Rows         []CompareRow `json:"rows"`
	Changes      []BugChange  `json:"changes"`        // sorted by bug id; never null
	BaseOnlyBugs []string     `json:"base_only_bugs"` // sorted; never null
	NewOnlyBugs  []string     `json:"new_only_bugs"`
}

// Comparison is the result of Compare.
type Comparison struct {
	Base           ReportInfo       `json:"base"`
	New            ReportInfo       `json:"new"`
	BaseOnlyAgents []string         `json:"base_only_agents"`
	NewOnlyAgents  []string         `json:"new_only_agents"`
	Pairs          []PairComparison `json:"pairs"`
}

// AgentName returns id without its last "@<version>" ("coder@2" -> "coder"); id itself if it has no "@".
func AgentName(id string) string {
	if i := strings.LastIndex(id, "@"); i >= 0 {
		return id[:i]
	}
	return id
}

// BugStatus is "error" when b.Graded == 0, "solved" when 2*b.Solved > b.Graded, else "unsolved".
func BugStatus(b BugAttempts) string {
	switch {
	case b.Graded == 0:
		return "error"
	case 2*b.Solved > b.Graded:
		return "solved"
	default:
		return "unsolved"
	}
}

// agentIDs returns the distinct agent ids of results, sorted.
func agentIDs(results []Result) []string {
	seen := map[string]bool{}
	ids := []string{}
	for _, r := range results {
		if !seen[r.Agent] {
			seen[r.Agent] = true
			ids = append(ids, r.Agent)
		}
	}
	sort.Strings(ids)
	return ids
}

// byName maps agent names to ids, failing when two ids share a name.
func byName(ids []string, side string) (map[string]string, error) {
	m := map[string]string{}
	for _, id := range ids { // ids are sorted
		n := AgentName(id)
		if prev, ok := m[n]; ok {
			return nil, fmt.Errorf("evals: compare: %s report has agents %q and %q with the same name; use --map", side, prev, id)
		}
		m[n] = id
	}
	return m, nil
}

// matchAgents returns the pairs to compare.
func matchAgents(baseIDs, newIDs []string, mapping []AgentPair) ([]AgentPair, error) {
	if len(mapping) > 0 {
		for _, p := range mapping {
			if !slices.Contains(baseIDs, p.Base) {
				return nil, fmt.Errorf("evals: compare: --map: agent %q is not in the base report", p.Base)
			}
			if !slices.Contains(newIDs, p.New) {
				return nil, fmt.Errorf("evals: compare: --map: agent %q is not in the new report", p.New)
			}
		}
		return mapping, nil
	}
	bn, err := byName(baseIDs, "base")
	if err != nil {
		return nil, err
	}
	nn, err := byName(newIDs, "new")
	if err != nil {
		return nil, err
	}
	var names []string
	for n := range bn {
		if _, ok := nn[n]; ok {
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("evals: compare: no agent name appears in both reports; use --map BASE_AGENT=NEW_AGENT")
	}
	sort.Strings(names)
	pairs := make([]AgentPair, 0, len(names))
	for _, n := range names {
		pairs = append(pairs, AgentPair{Base: bn[n], New: nn[n]})
	}
	return pairs, nil
}

func infoOf(r Report) ReportInfo {
	return ReportInfo{Suite: r.Suite, Results: len(r.Results), Repeat: max(1, r.Repeat)}
}

// Compare matches the agents of the two reports and compares each pair.
func Compare(base, newRep Report, mapping []AgentPair) (Comparison, error) {
	baseIDs, newIDs := agentIDs(base.Results), agentIDs(newRep.Results)
	pairs, err := matchAgents(baseIDs, newIDs, mapping)
	if err != nil {
		return Comparison{}, err
	}
	c := Comparison{
		Base: infoOf(base), New: infoOf(newRep),
		BaseOnlyAgents: []string{}, NewOnlyAgents: []string{}, Pairs: []PairComparison{},
	}
	usedBase, usedNew := map[string]bool{}, map[string]bool{}
	for _, p := range pairs {
		usedBase[p.Base], usedNew[p.New] = true, true
		c.Pairs = append(c.Pairs, comparePair(base.Results, newRep.Results, p))
	}
	for _, id := range baseIDs {
		if !usedBase[id] {
			c.BaseOnlyAgents = append(c.BaseOnlyAgents, id)
		}
	}
	for _, id := range newIDs {
		if !usedNew[id] {
			c.NewOnlyAgents = append(c.NewOnlyAgents, id)
		}
	}
	return c, nil
}

// agentResults returns the results of one agent id.
func agentResults(results []Result, id string) []Result {
	var out []Result
	for _, r := range results {
		if r.Agent == id {
			out = append(out, r)
		}
	}
	return out
}

// restrictBugs keeps the results whose bug is in keep.
func restrictBugs(results []Result, keep map[string]bool) []Result {
	var out []Result
	for _, r := range results {
		if keep[r.BugID] {
			out = append(out, r)
		}
	}
	return out
}

func bugSet(results []Result) map[string]bool {
	m := map[string]bool{}
	for _, r := range results {
		m[r.BugID] = true
	}
	return m
}

// onlyIn returns the sorted bug ids that are in a but not in b; never nil.
func onlyIn(a, b map[string]bool) []string {
	out := []string{}
	for id := range a {
		if !b[id] {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

func comparePair(baseRes, newRes []Result, p AgentPair) PairComparison {
	rb, rn := agentResults(baseRes, p.Base), agentResults(newRes, p.New)
	bb, bn := bugSet(rb), bugSet(rn)
	pc := PairComparison{
		AgentPair: p, Rows: []CompareRow{}, Changes: []BugChange{},
		BaseOnlyBugs: onlyIn(bb, bn), NewOnlyBugs: onlyIn(bn, bb),
	}
	common := map[string]bool{}
	for id := range bb {
		if bn[id] {
			common[id] = true
		}
	}
	if len(common) == 0 {
		return pc
	}
	sb := SummarizeAgents(restrictBugs(rb, common))[p.Base]
	sn := SummarizeAgents(restrictBugs(rn, common))[p.New]
	if pc.K = min(sb.MaxAttempts, sn.MaxAttempts); pc.K < 2 {
		pc.K = 0
	}
	for _, d := range []Difficulty{Easy, Medium, Hard} {
		n := 0
		for _, b := range sb.PerBug {
			if b.Difficulty == d {
				n++
			}
		}
		if n > 0 {
			pc.Rows = append(pc.Rows, CompareRow{string(d), n, sb.ByDifficulty[d], sn.ByDifficulty[d]})
		}
	}
	pc.Rows = append(pc.Rows, CompareRow{"all", len(common), sb.AgentBucket, sn.AgentBucket})
	pc.Changes = statusChanges(sb.PerBug, sn.PerBug)
	return pc
}

// statusChanges lists the bugs whose BugStatus differs; both inputs are
// sorted by bug id and hold the same bugs.
func statusChanges(base, newBugs []BugAttempts) []BugChange {
	changes := []BugChange{}
	for i, b := range base {
		n := newBugs[i]
		if bs, ns := BugStatus(b), BugStatus(n); bs != ns {
			changes = append(changes, BugChange{
				BugID: b.BugID, Difficulty: b.Difficulty, Base: b, New: n, BaseStatus: bs, NewStatus: ns,
			})
		}
	}
	return changes
}

// PrintComparison writes the text form of c.
func PrintComparison(w io.Writer, c Comparison) {
	fmt.Fprintf(w, "base: %s  suite %s  results %d  repeat %d\n", c.Base.Path, c.Base.Suite, c.Base.Results, c.Base.Repeat)
	fmt.Fprintf(w, "new:  %s  suite %s  results %d  repeat %d\n", c.New.Path, c.New.Suite, c.New.Results, c.New.Repeat)
	if c.Base.Suite != c.New.Suite {
		fmt.Fprintln(w, "warning: the reports are from different suites")
	}
	if len(c.BaseOnlyAgents) > 0 {
		fmt.Fprintf(w, "only in base: %s\n", strings.Join(c.BaseOnlyAgents, ", "))
	}
	if len(c.NewOnlyAgents) > 0 {
		fmt.Fprintf(w, "only in new: %s\n", strings.Join(c.NewOnlyAgents, ", "))
	}
	for _, p := range c.Pairs {
		printPair(w, p)
	}
}

func printPair(w io.Writer, p PairComparison) {
	common := 0
	if n := len(p.Rows); n > 0 {
		common = p.Rows[n-1].Bugs
	}
	fmt.Fprintf(w, "\n%s -> %s  bugs %d\n", p.Base, p.New, common)
	if len(p.BaseOnlyBugs) > 0 {
		fmt.Fprintf(w, "note: bugs only in base: %s\n", strings.Join(p.BaseOnlyBugs, ", "))
	}
	if len(p.NewOnlyBugs) > 0 {
		fmt.Fprintf(w, "note: bugs only in new: %s\n", strings.Join(p.NewOnlyBugs, ", "))
	}
	if len(p.Rows) == 0 {
		fmt.Fprintln(w, "no bug has results in both reports")
		return
	}
	printRows(w, p)
	printChanges(w, p.Changes)
}

func printRows(w io.Writer, p PairComparison) {
	tw := tabwriter.NewWriter(w, 0, 8, 2, ' ', 0)
	header := "DIFFICULTY\tBUGS\tPASS@1"
	if p.K > 0 {
		header += fmt.Sprintf("\tPASS@%d", p.K)
	}
	fmt.Fprintln(tw, header+"\tMEDIAN COST\tMEDIAN STEPS")
	for _, r := range p.Rows {
		cells := []string{r.Difficulty, strconv.Itoa(r.Bugs), passCell(r, 1)}
		if p.K > 0 {
			cells = append(cells, passCell(r, p.K))
		}
		cells = append(cells, costCell(r), stepsCell(r))
		fmt.Fprintln(tw, strings.Join(cells, "\t"))
	}
	_ = tw.Flush()
}

func passAt(b AgentBucket, k int) float64 {
	if k < 1 || k > len(b.PassAtK) {
		return 0
	}
	return b.PassAtK[k-1]
}

func passCell(r CompareRow, k int) string {
	b, n := passAt(r.Base, k), passAt(r.New, k)
	return fmt.Sprintf("%.3f -> %.3f (%+.3f)", b, n, n-b)
}

// signed formats a delta with an explicit sign (+ for zero) around body.
func signed(neg bool, body string) string {
	if neg {
		return "-" + body
	}
	return "+" + body
}

func costCell(r CompareRow) string {
	if r.Base.Solved == 0 || r.New.Solved == 0 {
		return fmt.Sprintf("%s -> %s (n/a)", costSide(r.Base), costSide(r.New))
	}
	d := r.New.MedianUSDMicrosSolved - r.Base.MedianUSDMicrosSolved
	amount := usd(max(d, -d))
	return fmt.Sprintf("%s -> %s (%s)", costSide(r.Base), costSide(r.New), signed(d < 0, amount))
}

func costSide(b AgentBucket) string {
	if b.Solved == 0 {
		return "-"
	}
	return usd(b.MedianUSDMicrosSolved)
}

func stepsCell(r CompareRow) string {
	if r.Base.Solved == 0 || r.New.Solved == 0 {
		return fmt.Sprintf("%s -> %s (n/a)", stepsSide(r.Base), stepsSide(r.New))
	}
	d := r.New.MedianStepsSolved - r.Base.MedianStepsSolved
	amount := strconv.FormatFloat(max(d, -d), 'f', -1, 64)
	return fmt.Sprintf("%s -> %s (%s)", stepsSide(r.Base), stepsSide(r.New), signed(d < 0, amount))
}

func stepsSide(b AgentBucket) string {
	if b.Solved == 0 {
		return "-"
	}
	return strconv.FormatFloat(b.MedianStepsSolved, 'f', -1, 64)
}

func printChanges(w io.Writer, changes []BugChange) {
	if len(changes) == 0 {
		fmt.Fprintln(w, "status changes: none")
		return
	}
	fmt.Fprintln(w, "status changes:")
	tw := tabwriter.NewWriter(w, 0, 8, 2, ' ', 0)
	for _, c := range changes {
		fmt.Fprintf(tw, "  %s\t%s -> %s\n", c.BugID, changeSide(c.BaseStatus, c.Base), changeSide(c.NewStatus, c.New))
	}
	_ = tw.Flush()
}

func changeSide(status string, b BugAttempts) string {
	if status == "error" {
		return "error"
	}
	return fmt.Sprintf("%s %d/%d", status, b.Solved, b.Graded)
}
