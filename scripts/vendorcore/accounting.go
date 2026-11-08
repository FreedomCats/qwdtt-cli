package main

// accounting describes intent for the offline guard, not conversion behaviour.
type accounting struct {
	id, class, rationale string
	proposals            []string
}

func localChange(id, rationale string) accounting {
	return accounting{id: id, class: "local", rationale: rationale}
}

func upstreamChange(id, rationale, proposal string) accounting {
	return accounting{id: id, class: "upstream", rationale: rationale, proposals: []string{proposal}}
}

type accountedFile struct {
	text       string
	accounting accounting
}

type logReplacement struct {
	from, to   string
	accounting accounting
}

type prunableImport struct {
	path       string
	accounting accounting
}

var pipelineAccounting = []accounting{
	localChange("strip-utf8-bom", "Read UTF-8 source and comparison targets without a byte order mark."),
	localChange("normalize-input-crlf", "Use LF before applying the line-anchored replacement patterns."),
	localChange("rewrite-package-main", "Extract upstream package main as the local core library."),
	localChange("generate-core-version", "Record the source version and conversion provenance alongside the library."),
	localChange("normalize-output-crlf", "Write every planned output with LF, including overrides and generated files."),
	localChange("ensure-output-newline", "Append a final LF when absent without removing existing trailing newlines."),
}

// accountedText binds a pipeline operation to its metadata without changing it.
func accountedText(id, value string) string { return value }
