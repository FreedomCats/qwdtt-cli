package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
)

const proposalDocument = "doc/upstream-proposals.md"
const reproductionReadme = "doc/upstream-repro/README.md"

var (
	proposalAnchor = regexp.MustCompile(`^<a id="(proposal-[a-z0-9-]+)"></a>$`)
	recordLink     = regexp.MustCompile(`\[[^\]]+\]\(([^()\s]+)\)`)
	inlineCode     = regexp.MustCompile("`([^`]+)`")
)

type proposalRecord struct {
	id, patch, repro string
	tests            []string
	location         string
}

func referencePath(document, target string) (string, error) {
	if target == "" || strings.ContainsAny(target, "\\:#?") || strings.HasPrefix(target, "/") {
		return "", fmt.Errorf("invalid relative artifact reference %q", target)
	}
	resolved := path.Join(path.Dir(document), target)
	if !fs.ValidPath(resolved) {
		return "", fmt.Errorf("artifact reference escapes repository: %q", target)
	}
	return resolved, nil
}

func linkedArtifact(document, record, suffix string) (string, error) {
	links := recordLink.FindAllStringSubmatch(record, -1)
	if len(links) != 1 {
		return "", fmt.Errorf("expected one unambiguous %s link", suffix)
	}
	target, err := referencePath(document, links[0][1])
	if err != nil {
		return "", err
	}
	if !strings.HasSuffix(target, suffix) {
		return "", fmt.Errorf("wrong artifact reference %q: expected %s", target, suffix)
	}
	return target, nil
}

func reproductionNames(record string) ([]string, error) {
	var names []string
	for _, match := range inlineCode.FindAllStringSubmatch(record, -1) {
		if !strings.HasPrefix(match[1], "TestRepro") {
			continue
		}
		if slices.Contains(names, match[1]) {
			return nil, fmt.Errorf("duplicate reproduction name %s", match[1])
		}
		names = append(names, match[1])
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("missing reproduction test names")
	}
	return names, nil
}

// Only explicit anchors and the accepted two record labels are interpreted.
// Continuations end at a blank line or the next record, not at a line wrap.
func readProposalRecords(source fs.FS) (map[string]proposalRecord, error) {
	data, err := fs.ReadFile(source, proposalDocument)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", proposalDocument, err)
	}
	lines := strings.Split(string(data), "\n")
	anchors := map[string]int{}
	var starts []int
	for i, line := range lines {
		match := proposalAnchor.FindStringSubmatch(strings.TrimSpace(line))
		if match == nil {
			continue
		}
		if _, exists := anchors[match[1]]; exists {
			return nil, fmt.Errorf("%s at %s:%d: duplicate anchor", match[1], proposalDocument, i+1)
		}
		anchors[match[1]] = i
		starts = append(starts, i)
	}
	starts = append(starts, len(lines))
	records := map[string]proposalRecord{}
	for id, start := range anchors {
		end := len(lines)
		for _, next := range starts {
			if next > start {
				end = next
				break
			}
		}
		heading := false
		for i := start + 1; i < end; i++ {
			if strings.HasPrefix(lines[i], "## ") {
				if heading {
					end = i
					break
				}
				heading = true
			}
		}
		location := fmt.Sprintf("%s:%d", proposalDocument, start+1)
		values := map[string][]string{}
		for i := start + 1; i < end; i++ {
			for _, label := range []string{"**Patch:**", "**Reproduction:**"} {
				line := strings.TrimSpace(lines[i])
				if !strings.HasPrefix(line, label) {
					continue
				}
				value := strings.TrimSpace(strings.TrimPrefix(line, label))
				for j := i + 1; j < end; j++ {
					next := strings.TrimSpace(lines[j])
					if next == "" || strings.HasPrefix(next, "**") || strings.HasPrefix(next, "#") || next == "---" {
						break
					}
					value += " " + next
				}
				values[label] = append(values[label], value)
			}
		}
		for _, label := range []string{"**Patch:**", "**Reproduction:**"} {
			if len(values[label]) != 1 {
				return nil, fmt.Errorf("%s at %s: missing or ambiguous %s record", id, location, label)
			}
		}
		patch, err := linkedArtifact(proposalDocument, values["**Patch:**"][0], ".patch")
		if err != nil {
			return nil, fmt.Errorf("%s at %s: Patch: %w", id, location, err)
		}
		repro, err := linkedArtifact(proposalDocument, values["**Reproduction:**"][0], "/repro_test.go")
		if err != nil {
			return nil, fmt.Errorf("%s at %s: Reproduction: %w", id, location, err)
		}
		names, err := reproductionNames(values["**Reproduction:**"][0])
		if err != nil {
			return nil, fmt.Errorf("%s at %s: %w", id, location, err)
		}
		records[id] = proposalRecord{id, patch, repro, names, location}
	}
	return records, nil
}

func checkReproductionDeclarations(source fs.FS, record proposalRecord) error {
	data, err := fs.ReadFile(source, record.repro)
	if err != nil {
		return fmt.Errorf("%s at %s: reproduction file %s: %w", record.id, record.location, record.repro, err)
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, record.repro, data, parser.ParseComments)
	if err != nil {
		return fmt.Errorf("%s at %s: %w", record.id, record.repro, err)
	}
	tagged := false
	for _, group := range file.Comments {
		for _, comment := range group.List {
			if comment.Text == "//go:build upstream_repro" && comment.Pos() < file.Package {
				tagged = true
			}
		}
	}
	if file.Name.Name != "main" || !tagged {
		return fmt.Errorf("%s at %s: expected tagged upstream package main reproduction", record.id, record.repro)
	}
	testingAlias := ""
	for _, imp := range file.Imports {
		value, _ := strconv.Unquote(imp.Path.Value)
		if value == "testing" {
			testingAlias = "testing"
			if imp.Name != nil {
				testingAlias = imp.Name.Name
			}
		}
	}
	for _, name := range record.tests {
		var declarations []*ast.FuncDecl
		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Name.Name == name {
				declarations = append(declarations, fn)
			}
		}
		if len(declarations) != 1 {
			return fmt.Errorf("%s at %s: missing or duplicate top-level reproduction test %s", record.id, record.repro, name)
		}
		fn := declarations[0]
		valid := fn.Type.TypeParams == nil && (fn.Type.Results == nil || len(fn.Type.Results.List) == 0) &&
			fn.Type.Params != nil && len(fn.Type.Params.List) == 1 && len(fn.Type.Params.List[0].Names) <= 1 && fn.Body != nil
		if valid {
			star, ok := fn.Type.Params.List[0].Type.(*ast.StarExpr)
			valid = ok && testingAlias != "" && testingAlias != "." && testingAlias != "_" && exprIs(star.X, testingAlias+".T")
		}
		if !valid {
			return fmt.Errorf("%s at %s: invalid test signature for %s; want func(*testing.T)", record.id, fset.Position(fn.Pos()), name)
		}
	}
	return nil
}

// Read only artifact rows and single-patch isolated-input rows. Selection of
// executable inputs remains the runner's responsibility; no runner state is read.
func checkReadmeReferences(source fs.FS, records map[string]proposalRecord) error {
	data, err := fs.ReadFile(source, reproductionReadme)
	if err != nil {
		return fmt.Errorf("%s: %w", reproductionReadme, err)
	}
	artifacts := map[string]bool{}
	associations := map[string][]string{}
	reproFiles := map[string]int{}
	for i, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "|") {
			continue
		}
		cells := strings.Split(line, "|")
		if len(cells) < 4 {
			continue
		}
		location := fmt.Sprintf("%s:%d", reproductionReadme, i+1)
		links := recordLink.FindAllStringSubmatch(cells[1], -1)
		if len(links) > 0 && strings.HasSuffix(links[0][1], ".patch") {
			patch, patchErr := linkedArtifact(reproductionReadme, cells[1], ".patch")
			var expected proposalRecord
			for _, candidate := range records {
				if candidate.patch == patch {
					expected = candidate
				}
			}
			backlinks := recordLink.FindAllStringSubmatch(cells[2], -1)
			if len(backlinks) != 1 {
				return fmt.Errorf("%s at %s: missing or ambiguous proposal backlink for %s", expected.id, location, links[0][1])
			}
			document, id, ok := strings.Cut(backlinks[0][1], "#")
			resolved, resolveErr := referencePath(reproductionReadme, document)
			if !ok || resolveErr != nil || resolved != proposalDocument {
				return fmt.Errorf("%s at %s: wrong proposal backlink %q", id, location, backlinks[0][1])
			}
			record, exists := records[id]
			if !exists {
				return fmt.Errorf("%s at %s: proposal backlink has no anchor", id, location)
			}
			if patchErr != nil || patch != record.patch {
				return fmt.Errorf("%s at %s: artifact patch disagrees with %s: %v", id, location, record.location, patchErr)
			}
			if artifacts[id] {
				return fmt.Errorf("%s at %s: duplicate artifact proposal backlink", id, location)
			}
			artifacts[id] = true
		}
		if len(links) > 0 && path.Base(links[0][1]) == "repro_test.go" {
			repro, err := linkedArtifact(reproductionReadme, cells[1], "/repro_test.go")
			if err != nil {
				return fmt.Errorf("%s: %w", location, err)
			}
			reproFiles[repro]++
		}
		patchNames := inlineCode.FindAllStringSubmatch(cells[2], -1)
		if len(patchNames) != 1 || !strings.HasSuffix(patchNames[0][1], ".patch") {
			continue
		}
		patch, err := referencePath(reproductionReadme, patchNames[0][1])
		if err != nil {
			return fmt.Errorf("%s: %w", location, err)
		}
		var record proposalRecord
		for _, candidate := range records {
			if candidate.patch == patch {
				record = candidate
			}
		}
		if record.id == "" {
			return fmt.Errorf("%s: isolated-input patch %s has no proposal record", location, patch)
		}
		names, err := reproductionNames(cells[3])
		if err != nil {
			return fmt.Errorf("%s at %s: isolated-input association: %w", record.id, location, err)
		}
		for _, name := range names {
			if !slices.Contains(record.tests, name) {
				return fmt.Errorf("%s at %s: isolated-input test %s disagrees with Reproduction record", record.id, location, name)
			}
			if slices.Contains(associations[record.id], name) {
				return fmt.Errorf("%s at %s: duplicate isolated-input association for %s", record.id, location, name)
			}
			associations[record.id] = append(associations[record.id], name)
		}
	}
	for id, record := range records {
		if !artifacts[id] {
			return fmt.Errorf("%s at %s: missing artifact patch/proposal backlink", id, reproductionReadme)
		}
		if reproFiles[record.repro] != 1 {
			return fmt.Errorf("%s at %s: missing or ambiguous reproduction artifact link to %s", id, reproductionReadme, record.repro)
		}
		for _, name := range record.tests {
			if !slices.Contains(associations[id], name) {
				return fmt.Errorf("%s at %s: missing isolated-input association for %s", id, reproductionReadme, name)
			}
		}
	}
	return nil
}

func validateProposalReferences(source fs.FS, entries []transformation) error {
	records, err := readProposalRecords(source)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		for _, id := range entry.proposals {
			if _, exists := records[id]; !exists {
				return fmt.Errorf("%s at %s: missing proposal anchor %s in %s", entry.id, entry.location, id, proposalDocument)
			}
		}
	}
	for _, record := range records {
		patch, err := fs.ReadFile(source, record.patch)
		if err != nil || len(patch) == 0 {
			return fmt.Errorf("%s at %s: missing or empty patch file %s: %v", record.id, record.location, record.patch, err)
		}
		if err := checkReproductionDeclarations(source, record); err != nil {
			return err
		}
	}
	return checkReadmeReferences(source, records)
}

func referenceFixture(t *testing.T) (fstest.MapFS, []transformation) {
	t.Helper()
	repository := os.DirFS("../..")
	records, err := readProposalRecords(repository)
	if err != nil {
		t.Fatal(err)
	}
	files := []string{proposalDocument, reproductionReadme}
	for _, record := range records {
		files = append(files, record.patch, record.repro)
	}
	fixture := fstest.MapFS{}
	for _, file := range files {
		data, err := fs.ReadFile(repository, file)
		if err != nil {
			t.Fatal(err)
		}
		fixture[file] = &fstest.MapFile{Data: data}
	}
	entries, err := validateAccounting(currentTables())
	if err != nil {
		t.Fatal(err)
	}
	return fixture, entries
}

func TestProposalReferenceMutations(t *testing.T) {
	const first = "proposal-1-closed-call"
	const repro = "doc/upstream-repro/repro_test.go"
	cases := []struct {
		name, diagnostic, id, location string
		mutate                         func(*testing.T, fstest.MapFS)
	}{
		{"removed anchor", "missing proposal anchor", first, proposalDocument, func(t *testing.T, f fstest.MapFS) {
			replaceFixture(t, f, proposalDocument, `<a id="proposal-1-closed-call"></a>`, "")
		}},
		{"renamed anchor", "missing proposal anchor", first, proposalDocument, func(t *testing.T, f fstest.MapFS) {
			replaceFixture(t, f, proposalDocument, `<a id="proposal-1-closed-call"></a>`, `<a id="proposal-renamed"></a>`)
		}},
		{"duplicate anchor", "duplicate anchor", first, proposalDocument, func(t *testing.T, f fstest.MapFS) {
			replaceFixture(t, f, proposalDocument, `<a id="proposal-1-closed-call"></a>`, "<a id=\"proposal-1-closed-call\"></a>\n<a id=\"proposal-1-closed-call\"></a>")
		}},
		{"missing patch record", "missing or ambiguous **Patch:**", first, proposalDocument, func(t *testing.T, f fstest.MapFS) {
			replaceFixture(t, f, proposalDocument, "**Patch:** [1-closed-call.patch](upstream-repro/1-closed-call.patch).", "")
		}},
		{"ambiguous patch record", "missing or ambiguous **Patch:**", first, proposalDocument, func(t *testing.T, f fstest.MapFS) {
			replaceFixture(t, f, proposalDocument, "**Patch:** [1-closed-call.patch](upstream-repro/1-closed-call.patch).", "**Patch:** [one](upstream-repro/1-closed-call.patch).\n**Patch:** [two](upstream-repro/2-baton.patch).")
		}},
		{"missing reproduction record", "missing or ambiguous **Reproduction:**", first, proposalDocument, func(t *testing.T, f fstest.MapFS) {
			replaceFixture(t, f, proposalDocument, "**Reproduction:** [repro_test.go](upstream-repro/repro_test.go), `TestReproClosedCallIsDead`;", "")
		}},
		{"duplicate reproduction record", "missing or ambiguous **Reproduction:**", first, proposalDocument, func(t *testing.T, f fstest.MapFS) {
			replaceFixture(t, f, proposalDocument, "isolated input `patched-closed-call`.", "isolated input `patched-closed-call`.\n**Reproduction:** [repro_test.go](upstream-repro/repro_test.go), `TestReproClosedCallIsDead`;")
		}},
		{"wrong patch reference", "artifact patch disagrees", first, reproductionReadme, func(t *testing.T, f fstest.MapFS) {
			replaceFixture(t, f, proposalDocument, "(upstream-repro/1-closed-call.patch)", "(upstream-repro/2-baton.patch)")
		}},
		{"wrong patch extension", "wrong artifact reference", first, proposalDocument, func(t *testing.T, f fstest.MapFS) {
			replaceFixture(t, f, proposalDocument, "(upstream-repro/1-closed-call.patch)", "(upstream-repro/README.md)")
		}},
		{"ambiguous patch link", "unambiguous .patch link", first, proposalDocument, func(t *testing.T, f fstest.MapFS) {
			replaceFixture(t, f, proposalDocument, "**Patch:** [1-closed-call.patch](upstream-repro/1-closed-call.patch).", "**Patch:** [one](upstream-repro/1-closed-call.patch), [two](upstream-repro/2-baton.patch).")
		}},
		{"missing patch file", "patch file", first, proposalDocument, func(t *testing.T, f fstest.MapFS) { delete(f, "doc/upstream-repro/1-closed-call.patch") }},
		{"renamed patch file", "patch file", first, proposalDocument, func(t *testing.T, f fstest.MapFS) {
			f["doc/upstream-repro/renamed.patch"] = f["doc/upstream-repro/1-closed-call.patch"]
			delete(f, "doc/upstream-repro/1-closed-call.patch")
		}},
		{"wrong repro reference", "wrong artifact reference", first, proposalDocument, func(t *testing.T, f fstest.MapFS) {
			replaceFixture(t, f, proposalDocument, "**Reproduction:** [repro_test.go](upstream-repro/repro_test.go), `TestReproClosedCallIsDead`;", "**Reproduction:** [other](upstream-repro/README.md), `TestReproClosedCallIsDead`;")
		}},
		{"ambiguous repro link", "unambiguous /repro_test.go link", first, proposalDocument, func(t *testing.T, f fstest.MapFS) {
			replaceFixture(t, f, proposalDocument, "**Reproduction:** [repro_test.go](upstream-repro/repro_test.go), `TestReproClosedCallIsDead`;", "**Reproduction:** [one](upstream-repro/repro_test.go), [two](upstream-repro/repro_test.go), `TestReproClosedCallIsDead`;")
		}},
		{"wrong existing repro file", "reproduction artifact link", first, reproductionReadme, func(t *testing.T, f fstest.MapFS) {
			f["doc/other/repro_test.go"] = &fstest.MapFile{Data: slices.Clone(f[repro].Data)}
			replaceFixture(t, f, proposalDocument, "**Reproduction:** [repro_test.go](upstream-repro/repro_test.go), `TestReproClosedCallIsDead`;", "**Reproduction:** [repro_test.go](other/repro_test.go), `TestReproClosedCallIsDead`;")
		}},
		{"missing reproduction names", "missing reproduction test names", first, proposalDocument, func(t *testing.T, f fstest.MapFS) {
			replaceFixture(t, f, proposalDocument, "`TestReproClosedCallIsDead`;", "no declared test;")
		}},
		{"duplicate reproduction names", "duplicate reproduction name", first, proposalDocument, func(t *testing.T, f fstest.MapFS) {
			replaceFixture(t, f, proposalDocument, "`TestReproClosedCallIsDead`;", "`TestReproClosedCallIsDead`, `TestReproClosedCallIsDead`;")
		}},
		{"missing repro file", "reproduction file", "proposal-", proposalDocument, func(t *testing.T, f fstest.MapFS) { delete(f, repro) }},
		{"renamed repro file", "reproduction file", "proposal-", proposalDocument, func(t *testing.T, f fstest.MapFS) {
			f["doc/upstream-repro/renamed_test.go"] = f[repro]
			delete(f, repro)
		}},
		{"renamed test", "top-level reproduction test TestReproClosedCallIsDead", first, repro, func(t *testing.T, f fstest.MapFS) {
			replaceFixture(t, f, repro, "func TestReproClosedCallIsDead(", "func TestReproRenamed(")
		}},
		{"removed test", "top-level reproduction test TestReproClosedCallIsDead", first, repro, func(t *testing.T, f fstest.MapFS) {
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, repro, f[repro].Data, 0)
			if err != nil {
				t.Fatal(err)
			}
			for _, decl := range file.Decls {
				if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "TestReproClosedCallIsDead" {
					start, end := fset.Position(fn.Pos()).Offset, fset.Position(fn.End()).Offset
					data := slices.Clone(f[repro].Data)
					f[repro] = &fstest.MapFile{Data: append(data[:start], data[end:]...)}
					return
				}
			}
			t.Fatal("missing fixture test declaration")
		}},
		{"duplicate test", "duplicate top-level reproduction test", first, repro, func(t *testing.T, f fstest.MapFS) {
			f[repro] = &fstest.MapFile{Data: append(slices.Clone(f[repro].Data), []byte("\nfunc TestReproClosedCallIsDead(t *testing.T) {}\n")...)}
		}},
		{"wrong test parameter", "invalid test signature", first, repro, func(t *testing.T, f fstest.MapFS) {
			replaceFixture(t, f, repro, "func TestReproClosedCallIsDead(t *testing.T)", "func TestReproClosedCallIsDead(t testing.T)")
		}},
		{"test result", "invalid test signature", first, repro, func(t *testing.T, f fstest.MapFS) {
			replaceFixture(t, f, repro, "func TestReproClosedCallIsDead(t *testing.T)", "func TestReproClosedCallIsDead(t *testing.T) int")
		}},
		{"missing test parameter", "invalid test signature", first, repro, func(t *testing.T, f fstest.MapFS) {
			replaceFixture(t, f, repro, "func TestReproClosedCallIsDead(t *testing.T)", "func TestReproClosedCallIsDead()")
		}},
		{"method instead of test", "top-level reproduction test", first, repro, func(t *testing.T, f fstest.MapFS) {
			replaceFixture(t, f, repro, "func TestReproClosedCallIsDead(", "func (r *synthetic) TestReproClosedCallIsDead(")
		}},
		{"missing build tag", "expected tagged upstream", "proposal-", repro, func(t *testing.T, f fstest.MapFS) {
			replaceFixture(t, f, repro, "//go:build upstream_repro", "// fixture without tag")
		}},
		{"unmatched continued test", "missing isolated-input association for TestReproNewCounter", "proposal-4-dropped-packets", reproductionReadme, func(t *testing.T, f fstest.MapFS) {
			replaceFixture(t, f, proposalDocument, "`TestReproDroppedDown`, `TestReproDroppedStatsOutput`;", "`TestReproDroppedDown`, `TestReproDroppedStatsOutput`, `TestReproNewCounter`;")
			f[repro] = &fstest.MapFile{Data: append(slices.Clone(f[repro].Data), []byte("\nfunc TestReproNewCounter(t *testing.T) {}\n")...)}
			// The other continued names are parsed too; the unmatched added test is diagnosed.
		}},
		{"README wrong backlink", "proposal backlink has no anchor", "proposal-renamed", reproductionReadme, func(t *testing.T, f fstest.MapFS) {
			replaceFixture(t, f, reproductionReadme, "#proposal-1-closed-call)", "#proposal-renamed)")
		}},
		{"README missing backlink", "missing or ambiguous proposal backlink", first, reproductionReadme, func(t *testing.T, f fstest.MapFS) {
			replaceFixture(t, f, reproductionReadme, "[Proposal 1](../upstream-proposals.md#proposal-1-closed-call)", "Proposal 1")
		}},
		{"README wrong document", "wrong proposal backlink", first, reproductionReadme, func(t *testing.T, f fstest.MapFS) {
			replaceFixture(t, f, reproductionReadme, "../upstream-proposals.md#proposal-1-closed-call", "../architecture.md#proposal-1-closed-call")
		}},
		{"README wrong patch", "artifact patch disagrees", first, reproductionReadme, func(t *testing.T, f fstest.MapFS) {
			replaceFixture(t, f, reproductionReadme, "[1-closed-call.patch](1-closed-call.patch)", "[1-closed-call.patch](2-baton.patch)")
		}},
		{"README missing artifact", "missing artifact patch/proposal backlink", first, reproductionReadme, func(t *testing.T, f fstest.MapFS) {
			replaceFixture(t, f, reproductionReadme, "| [1-closed-call.patch](1-closed-call.patch)", "removed [1-closed-call.patch](1-closed-call.patch)")
		}},
		{"README duplicate artifact", "duplicate artifact proposal backlink", first, reproductionReadme, func(t *testing.T, f fstest.MapFS) {
			f[reproductionReadme] = &fstest.MapFile{Data: append(slices.Clone(f[reproductionReadme].Data), []byte("\n| [1-closed-call.patch](1-closed-call.patch) | [Proposal](../upstream-proposals.md#proposal-1-closed-call) |\n")...)}
		}},
		{"README repro link", "reproduction artifact link", "proposal-", reproductionReadme, func(t *testing.T, f fstest.MapFS) {
			replaceFixture(t, f, reproductionReadme, "[repro_test.go](repro_test.go)", "[repro_test.go](missing/repro_test.go)")
		}},
		{"README wrong association", "isolated-input test TestReproGroupPassesBatonOnCredsFailure disagrees", first, reproductionReadme, func(t *testing.T, f fstest.MapFS) {
			replaceFixture(t, f, reproductionReadme, "| `TestReproClosedCallIsDead`: pass |", "| `TestReproGroupPassesBatonOnCredsFailure`: pass |")
		}},
		{"README missing association", "missing isolated-input association", first, reproductionReadme, func(t *testing.T, f fstest.MapFS) {
			replaceFixture(t, f, reproductionReadme, "| `patched-closed-call` |", "removed `patched-closed-call` |")
		}},
		{"README duplicate association", "duplicate isolated-input association", first, reproductionReadme, func(t *testing.T, f fstest.MapFS) {
			f[reproductionReadme] = &fstest.MapFile{Data: append(slices.Clone(f[reproductionReadme].Data), []byte("\n| `extra` | `1-closed-call.patch` | `TestReproClosedCallIsDead`: pass |\n")...)}
		}},
	}
	fixture, entries := referenceFixture(t)
	if err := validateProposalReferences(fixture, entries); err != nil {
		t.Fatalf("valid fixture: %v", err)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture, entries := referenceFixture(t)
			tc.mutate(t, fixture)
			requireDiagnostic(t, validateProposalReferences(fixture, entries), tc.diagnostic, tc.id, tc.location)
		})
	}
}
