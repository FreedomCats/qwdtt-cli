# Project rules for agents

## Language

Everything new in the repository is in English: documentation in `doc/`, new code comments,
tests, `AGENTS.md`, project memory and working notes (`doc/local/`), the roadmap node
headings (`## Task`, `## Steps`, `## Done when`, `## Outcome`) and commit messages.

In Russian stay `README.md` and the documents already written: `architecture.md`,
`comparison.md`, `e2-captcha.md`, `autoclick-captcha.md`, `upstream-proposals.md` and the
stage logs `doc/local/plan-*.md`. They are not translated as such: a document moves to
English when it is edited on substance.

Conversation with the user is in the user's language (Russian), not in English.

This is a personal, non-commercial project: it has no Jira and no issue numbers.

## Personal data — never in the repository

**This is about the developer's safety, not tidiness.** If the developer is identified,
their life, health and freedom are at risk. Nothing that could identify them may get into
the repository: real name, email, IP addresses, profile names, employer, call hashes,
details of the work environment and OS, user name — and in general any detail that makes
them easy to find. When in doubt, do not write it down.

The repository is public. Documentation (`README.md`, examples), code, tests, comments and
commit messages **must not** contain:

- the user's real name, their emails (personal and work), the name of their personal
  GitHub account and other accounts, their employer;
- the developer's environment: the distribution and machine the work is done on, user
  name, groups, home directories (`/home/…`, `C:\Users\…`), work networks;
- dates and times of work: when something was done, checked, compared;
- the user's external IP addresses: home address and VPS address;
- the name and address of the qWDTT server (domain, IP, port);
- home network addresses (ULA prefix, local machine addresses) and corporate network
  addresses (corporate VPN server, corporate DNS, search domains);
- VK call hashes and links to calls — neither in full nor shortened, as the log prints them
  (`abcdEF-1…`);
- the user's profile names. The exception is `home`: it is the example name in code, tests
  and README and may stay;
- passwords, keys and any values from the user's `secrets.json` and `profiles.toml`.

Use words or placeholders instead: "VPS address", "home address", "qWDTT server",
`<profile>`, `<hash>`, `10.x`, `192.168.1.x`, `fd…`, user `user`. In tests — documentation
addresses (`192.0.2.0/24`, `198.51.100.0/24`, `203.0.113.0/24`, `2001:db8::/32`) and
made-up values.

These values may be shown to the user in chat. Not in repository files, even if the old
documents did it that way.

## Documents in `doc/`: what is published and what is not

Everything directly in `doc/` is in the repository: the cleaned `architecture.md`,
`comparison.md`, `e2-captcha.md`, `upstream-proposals.md`, `autoclick-captcha.md`, and
`doc/upstream-repro/`: tests, patches and the reproduction script for
`upstream-proposals.md`. All the rules above apply to these documents: no dates of work, no
time of day ("at night", "late"), no environment or machine, no names or paths — only the
substance. Results of live checks may be written down — without the environment, link
speeds and addresses; the day-by-day course of work goes only into the logs.

Two subdirectories are outside git (`.gitignore`):

- `doc/local/` — stage logs `plan-e*.md` and `plan-*.md` (they inevitably contain the
  environment and dates), the roadmap journal (`doc/local/journal.md`), working notes and
  copies of documents before cleanup;
- `doc/runs/` — scripts and logs of live runs.

Check a new document for publication against this file before `git add`; put a draft with
environment details into `doc/local/`.

## The roadmap

The work is led by a stage-and-task tree in `doc/roadmap/`: `doc/roadmap/README.md` lists
the roadmaps, each roadmap is a directory with a root `plan.md` and stages and tasks under
it. The notation (`kind`, `status`, `base`, `depends`, the sections of a node) is described
in the `lead` agent's rules.

The roadmap is published like the rest of `doc/`, with one exception: its journal with dated
decisions and the resume point — "where we stopped" — is `doc/local/journal.md`, outside
git. Nodes in the repository carry decisions and results without dates or environment; the
course of work stays in `doc/local/`.

## Commits

The first line is a plain English description of the change, without an issue number: the
project has no Jira and no task keys.

After the first line and an empty line comes the body, in English: what was done and why.
Write so that a colleague understands the change without opening the diff. The main thing is
why: which problem it solves, what was wrong before, what works differently now. If a choice
had to be made, say briefly why this option. Do not retell the diff line by line and do not
list files without need. Identifiers, file names and commands stay as they are.

Author and committer — only `FreedomCats <noreply@freedomcats.invalid>` (set in the
repository's `.git/config`). The date is today's, but without the time: midnight UTC.
Neither the time of day nor the time zone gets into the history:

    d=$(date -u +%F)T00:00:00Z
    GIT_AUTHOR_DATE=$d GIT_COMMITTER_DATE=$d git commit …

The `.invalid` domain is reserved (RFC 2606): nobody has such an email, and GitHub will
not attribute the commits to someone else's account.

No `Co-Authored-By` trailer and no other attribution lines naming the agent.

The same applies at `--amend`, `rebase` and `cherry-pick`: git sets the committer date to
the current time.

Release tags (`v*`, by them CI builds the release) — lightweight only: `git tag v0.1.0`,
without `-a`, `-s` and `-m`. An annotated tag stores the name, email and date of whoever
created it.

**Before every commit** check `git diff --cached` for everything listed above: addresses
obtained in live runs, values from `~/.config/qwdtt-cli/`, the server's name, hashes, paths
and names from the developer's machine, dates. After the commit —
`git log -1 --format='%an <%ae> %ad | %cn <%ce> %cd' --date=iso-strict`. The forbidden
values themselves are never written into this file or into check scripts.

If something like that does get into the history — tell the user immediately. Cleaning the
history (`git filter-repo`) rewrites all commit hashes and is done only with the user's
consent.
