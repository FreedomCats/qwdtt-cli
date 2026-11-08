package main

import (
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

type accountingTables struct {
	excluded  []excludedFile
	overrides map[string]accountedFile
	generated map[string]accountedFile
	rules     []rule
	logs      []logReplacement
	imports   []prunableImport
	pipeline  []accounting
}

func currentTables() accountingTables {
	return accountingTables{excludedFiles, fileOverrides, generatedFiles, replacements, logReplacements, prunableImports, pipelineAccounting}
}

func cloneAccounting(a accounting) accounting {
	a.proposals = slices.Clone(a.proposals)
	return a
}

func (tables accountingTables) clone() accountingTables {
	tables.excluded = slices.Clone(tables.excluded)
	for i := range tables.excluded {
		tables.excluded[i].accounting = cloneAccounting(tables.excluded[i].accounting)
	}
	cloneFiles := func(files map[string]accountedFile) map[string]accountedFile {
		out := make(map[string]accountedFile, len(files))
		for k, v := range files {
			v.accounting = cloneAccounting(v.accounting)
			out[k] = v
		}
		return out
	}
	tables.overrides = cloneFiles(tables.overrides)
	tables.generated = cloneFiles(tables.generated)
	tables.rules = slices.Clone(tables.rules)
	for i := range tables.rules {
		tables.rules[i].accounting = cloneAccounting(tables.rules[i].accounting)
		if tables.rules[i].Replace != nil {
			tables.rules[i].Replace = to(*tables.rules[i].Replace)
		}
	}
	tables.logs = slices.Clone(tables.logs)
	for i := range tables.logs {
		tables.logs[i].accounting = cloneAccounting(tables.logs[i].accounting)
	}
	tables.imports = slices.Clone(tables.imports)
	for i := range tables.imports {
		tables.imports[i].accounting = cloneAccounting(tables.imports[i].accounting)
	}
	tables.pipeline = slices.Clone(tables.pipeline)
	for i := range tables.pipeline {
		tables.pipeline[i] = cloneAccounting(tables.pipeline[i])
	}
	return tables
}

type transformation struct {
	accounting
	location string
}

// Traverse the actual tables, including additions without metadata. Natural keys
// are checked independently of IDs; file matching has the converter's case rules.
func collectAccounting(tables accountingTables) ([]transformation, error) {
	var entries []transformation
	keys := map[string][]string{}
	add := func(family, key string, a accounting, fileKey bool) error {
		location := "scripts/vendorcore/rules.go (" + family + "/" + key + ")"
		if family == "pipeline" {
			location = "scripts/vendorcore/accounting.go (pipeline/" + key + ")"
		}
		if strings.TrimSpace(key) == "" {
			return fmt.Errorf("%s: empty identity", location)
		}
		for _, previous := range keys[family] {
			if previous == key || fileKey && strings.EqualFold(previous, key) {
				return fmt.Errorf("%s: duplicate identity %q", location, key)
			}
		}
		keys[family] = append(keys[family], key)
		if a.id == "" {
			return fmt.Errorf("%s: missing accounting", location)
		}
		if family != "replacements" && family != "pipeline" {
			want := family + "/" + key
			if a.id != want && !(fileKey && strings.EqualFold(a.id, want)) {
				return fmt.Errorf("%s: accounting ID %q does not match natural identity %q", location, a.id, want)
			}
		}
		entries = append(entries, transformation{a, location})
		return nil
	}
	for _, f := range tables.excluded {
		if err := add("excludedFiles", f.Name, f.accounting, true); err != nil {
			return nil, err
		}
	}
	for family, files := range map[string]map[string]accountedFile{"fileOverrides": tables.overrides, "generatedFiles": tables.generated} {
		for name, f := range files {
			if err := add(family, name, f.accounting, true); err != nil {
				return nil, err
			}
		}
	}
	for _, r := range tables.rules {
		key := r.accounting.id
		if key == "" {
			key = r.File + " (unclassified rule)"
		}
		if err := add("replacements", key, r.accounting, false); err != nil {
			return nil, err
		}
		if !regexp.MustCompile(`^replacements/[a-z][a-z0-9]*(-[a-z0-9]+)*$`).MatchString(r.accounting.id) {
			return nil, fmt.Errorf("%s at scripts/vendorcore/rules.go: replacement needs a descriptive stable ID", r.accounting.id)
		}
	}
	for _, r := range tables.logs {
		if err := add("logReplacements", r.from, r.accounting, false); err != nil {
			return nil, err
		}
	}
	for _, imp := range tables.imports {
		if err := add("prunableImports", imp.path, imp.accounting, false); err != nil {
			return nil, err
		}
	}
	for _, a := range tables.pipeline {
		if err := add("pipeline", a.id, a, false); err != nil {
			return nil, err
		}
	}
	return entries, nil
}

func validateAccounting(tables accountingTables) ([]transformation, error) {
	entries, err := collectAccounting(tables)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, entry := range entries {
		a := entry.accounting
		fail := func(reason string) ([]transformation, error) {
			return nil, fmt.Errorf("%s at %s: %s", a.id, entry.location, reason)
		}
		if a.id != strings.TrimSpace(a.id) {
			return fail("invalid identity whitespace")
		}
		if seen[a.id] {
			return fail("duplicate accounting ID")
		}
		seen[a.id] = true
		if len(strings.Fields(a.rationale)) < 3 {
			return fail("rationale must explain the transformation")
		}
		switch a.class {
		case "local":
			if len(a.proposals) != 0 {
				return fail("local accounting has proposal references")
			}
		case "upstream", "mixed":
			if len(a.proposals) == 0 {
				return fail("missing required proposal references")
			}
		default:
			return fail("invalid class " + a.class)
		}
		refs := map[string]bool{}
		for _, id := range a.proposals {
			if !regexp.MustCompile(`^proposal-[a-z0-9]+(-[a-z0-9]+)*$`).MatchString(id) {
				return fail("invalid proposal identity " + id)
			}
			if refs[id] {
				return fail("duplicate proposal reference " + id)
			}
			refs[id] = true
		}
	}

	const mixedID = "replacements/credentials-failure-handoff"
	foundMixed := false
	for _, r := range tables.rules {
		if r.accounting.id != mixedID {
			continue
		}
		foundMixed = true
		a := r.accounting
		if a.class != "mixed" || !slices.Contains(a.proposals, "proposal-2-baton") ||
			!strings.EqualFold(r.File, "group.go") ||
			!strings.Contains(r.InsertAfter, "emitHashFailed(hash, groupID, err)") ||
			!strings.Contains(r.InsertAfter, "close(signalReady)") ||
			!strings.Contains(a.rationale, "emitHashFailed") || !strings.Contains(a.rationale, "close(signalReady)") {
			return nil, fmt.Errorf("%s at scripts/vendorcore/rules.go: mixed handoff must retain local failure event and upstream baton operation/proposal", mixedID)
		}
	}
	if !foundMixed {
		return nil, fmt.Errorf("%s at scripts/vendorcore/rules.go: missing mixed handoff rule", mixedID)
	}
	return entries, nil
}

func requireDiagnostic(t *testing.T, err error, fragments ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("mutation unexpectedly passed")
	}
	for _, fragment := range fragments {
		if !strings.Contains(err.Error(), fragment) {
			t.Errorf("error %q does not contain %q", err, fragment)
		}
	}
}

func TestTransformationAccounting(t *testing.T) {
	entries, err := validateAccounting(currentTables())
	if err != nil {
		t.Fatal(err)
	}
	if err := validatePipeline(os.DirFS("."), pipelineAccounting); err != nil {
		t.Fatal(err)
	}
	if err := validateProposalReferences(os.DirFS("../.."), entries); err != nil {
		t.Fatal(err)
	}
}

func accountingFixture(t *testing.T) accountingTables {
	t.Helper()
	tables := currentTables().clone()
	if _, err := validateAccounting(tables); err != nil {
		t.Fatalf("valid table fixture: %v", err)
	}
	return tables
}

func TestUnclassifiedTableAdditions(t *testing.T) {
	base := accountingFixture(t)
	cases := map[string]func(*accountingTables){
		"excludedFiles": func(v *accountingTables) {
			v.excluded = append(v.excluded, excludedFile{Name: "extra.go", Reason: "synthetic exclusion"})
		},
		"fileOverrides":  func(v *accountingTables) { v.overrides["extra.go"] = accountedFile{text: "package core\n"} },
		"generatedFiles": func(v *accountingTables) { v.generated["extra.go"] = accountedFile{text: "package core\n"} },
		"replacements": func(v *accountingTables) {
			v.rules = append(v.rules, rule{File: "extra.go", Count: 1, Pattern: "synthetic", Replace: to("replacement")})
		},
		"logReplacements": func(v *accountingTables) {
			v.logs = append(v.logs, logReplacement{from: "extraLog(", to: "localLog("})
		},
		"prunableImports": func(v *accountingTables) { v.imports = append(v.imports, prunableImport{path: "extra/import"}) },
	}
	for family, mutate := range cases {
		t.Run(family, func(t *testing.T) {
			v := base.clone()
			mutate(&v)
			_, err := validateAccounting(v)
			requireDiagnostic(t, err, family, "missing accounting")
		})
	}
}

func TestAccountingMutations(t *testing.T) {
	base := accountingFixture(t)
	cases := []struct {
		name, diagnostic string
		mutate           func(*accountingTables)
	}{
		{"duplicate rule", "duplicate identity", func(v *accountingTables) { v.rules = append(v.rules, v.rules[0]) }},
		{"duplicate accounting", "duplicate accounting ID", func(v *accountingTables) { v.pipeline[0].id = v.rules[0].accounting.id }},
		{"duplicate pipeline", "duplicate identity", func(v *accountingTables) { v.pipeline = append(v.pipeline, v.pipeline[0]) }},
		{"case-fold exclusion", "duplicate identity", func(v *accountingTables) {
			f := v.excluded[0]
			f.Name = strings.ToUpper(f.Name)
			v.excluded = append(v.excluded, f)
		}},
		{"case-fold override", "duplicate identity", func(v *accountingTables) { v.overrides["LISTEN.GO"] = v.overrides["listen.go"] }},
		{"case-fold generated", "duplicate identity", func(v *accountingTables) { v.generated["LISTEN_UNIX.GO"] = v.generated["listen_unix.go"] }},
		{"duplicate log key", "duplicate identity", func(v *accountingTables) { v.logs = append(v.logs, v.logs[0]) }},
		{"duplicate import key", "duplicate identity", func(v *accountingTables) { v.imports = append(v.imports, v.imports[0]) }},
		{"wrong natural ID", "natural identity", func(v *accountingTables) { v.imports[0].accounting.id = "prunableImports/other" }},
		{"index rule ID", "descriptive stable ID", func(v *accountingTables) { v.rules[0].accounting.id = "replacements/0" }},
		{"invalid class", "invalid class", func(v *accountingTables) { v.rules[0].accounting.class = "unknown" }},
		{"empty rationale", "rationale", func(v *accountingTables) { v.rules[0].accounting.rationale = "" }},
		{"whitespace rationale", "rationale", func(v *accountingTables) { v.rules[0].accounting.rationale = " \n\t " }},
		{"local refs", "local accounting", func(v *accountingTables) { v.rules[0].accounting.proposals = []string{"proposal-1-closed-call"} }},
		{"missing refs", "missing required proposal", func(v *accountingTables) { v.rules[0].accounting.class = "upstream" }},
		{"mixed missing refs", "missing required proposal", func(v *accountingTables) { v.rules[0].accounting.class = "mixed" }},
		{"empty ref", "invalid proposal identity", func(v *accountingTables) {
			v.rules[0].accounting.class, v.rules[0].accounting.proposals = "upstream", []string{" "}
		}},
		{"duplicate refs", "duplicate proposal reference", func(v *accountingTables) {
			v.rules[0].accounting.class = "upstream"
			v.rules[0].accounting.proposals = []string{"proposal-1-closed-call", "proposal-1-closed-call"}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := base.clone()
			tc.mutate(&v)
			_, err := validateAccounting(v)
			requireDiagnostic(t, err, tc.diagnostic)
		})
	}
}

func TestMixedHandoffAccounting(t *testing.T) {
	base := accountingFixture(t)
	for _, change := range []string{"local", "upstream", "wrong proposal", "event removed", "baton removed", "purpose omitted", "renamed ID"} {
		t.Run(change, func(t *testing.T) {
			v := base.clone()
			for i := range v.rules {
				r := &v.rules[i]
				if r.accounting.id != "replacements/credentials-failure-handoff" {
					continue
				}
				switch change {
				case "local":
					r.accounting.class, r.accounting.proposals = "local", nil
				case "upstream":
					r.accounting.class = "upstream"
				case "wrong proposal":
					r.accounting.proposals = []string{"proposal-1-closed-call"}
				case "event removed":
					r.InsertAfter = strings.ReplaceAll(r.InsertAfter, "emitHashFailed", "otherEvent")
				case "baton removed":
					r.InsertAfter = strings.ReplaceAll(r.InsertAfter, "close(signalReady)", "otherOperation()")
				case "purpose omitted":
					r.accounting.rationale = "Fix upstream baton handoff only."
				case "renamed ID":
					r.accounting.id = "replacements/renamed-handoff"
				}
			}
			_, err := validateAccounting(v)
			requireDiagnostic(t, err, "replacements/credentials-failure-handoff", "mixed handoff")
		})
	}
}

func TestAccountingAllowsManyToOne(t *testing.T) {
	v := currentTables().clone()
	for _, id := range []string{"first-extra-rule", "second-extra-rule"} {
		v.rules = append(v.rules, rule{File: "extra.go", Count: 1, Pattern: "synthetic", Replace: to("replacement"),
			accounting: upstreamChange("replacements/"+id, "Share the coherent closed-call proposal across related transformations.", "proposal-1-closed-call")})
	}
	if _, err := validateAccounting(v); err != nil {
		t.Fatal(err)
	}
}
