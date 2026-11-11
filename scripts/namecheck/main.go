// Command namecheck scans text for client terms held outside the
// repository. It never prints a term, the matched text, or the line.
//
// Usage:
//
//	namecheck [--staged | --msg FILE | --range REVS | --names | --history | --tag] [PATH...]
//
// --range takes a list of revisions separated by white space, as git
// rev-list reads them, for example "SHA ^REMOTE_REF". --names scans each
// PATH argument as a name only, without reading a file, for branch and tag
// names.
//
// --tag takes two arguments, a ref name and an object name. When the object
// is an annotated tag, or a chain of them, it scans each tag's tagger and
// message. An object of any other kind is left alone.
//
// The hidden flag --C DIR runs the git-backed modes in DIR instead of the
// process's working directory. Tests use it so that parallel tests can each
// scan their own repository without changing the shared working directory.
//
// A finding never quotes a term. Any path component that holds a match is
// printed as ***.
//
// Exit codes are 0 when clean, 1 on CLIENT_NAME_FOUND, and 2 when the
// term list is unavailable (CLIENT_TERMS_UNAVAILABLE) or the usage is wrong.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

func main() {
	os.Exit(run(os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

type options struct {
	staged    bool
	msgMode   bool
	msg       string
	rangeMode bool
	rng       string
	history   bool
	names     bool
	tag       bool
	dir       string
	paths     []string
}

func parseArgs(args []string) (options, error) {
	var o options

	modes := 0

	switches := map[string]*bool{
		"--staged":  &o.staged,
		"--names":   &o.names,
		"--tag":     &o.tag,
		"--history": &o.history,
	}

	for i := 0; i < len(args); i++ {
		a := args[i]
		value := func() (string, error) {
			if i+1 >= len(args) {
				return "", fmt.Errorf("%s needs a value", a)
			}

			i++

			return args[i], nil
		}

		if flag, ok := switches[a]; ok {
			*flag = true
			modes++

			continue
		}

		var err error

		switch a {
		case "--msg":
			o.msgMode = true

			o.msg, err = value()
			if err == nil && o.msg == "" {
				err = errors.New("--msg needs a non-empty value")
			}

			modes++
		case "--range":
			o.rangeMode = true

			o.rng, err = value()
			if err == nil && o.rng == "" {
				err = errors.New("--range needs a non-empty value")
			}

			modes++
		case "--C":
			o.dir, err = value()
		default:
			if len(a) > 1 && a[0] == '-' {
				return o, fmt.Errorf("unknown flag %s", a)
			}

			o.paths = append(o.paths, a)
		}

		if err != nil {
			return o, err
		}
	}

	if o.tag && len(o.paths) != 2 {
		return o, errors.New("--tag needs a ref name and an object name")
	}

	if modes > 1 {
		return o, errors.New("use at most one of --staged, --msg, --range, --names, --history, --tag")
	}

	return o, nil
}

func run(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	opts, err := parseArgs(args)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "namecheck: %v\n", err)

		return 2
	}

	path, ok := termsPath(getenv)
	if !ok {
		_, _ = fmt.Fprintln(stderr, errTermsUnavailable)

		return 2
	}

	terms, err := loadTerms(path)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)

		return 2
	}

	allow, err := loadAllow(opts.dir)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)

		return 2
	}

	s := &scanner{terms: terms, allow: allow, dir: opts.dir, out: func(l string) { _, _ = fmt.Fprintln(stdout, l) }}
	switch {
	case opts.staged:
		err = s.scanStaged(opts.paths)
	case opts.msgMode:
		err = s.scanMessageFile(opts.msg)
	case opts.rangeMode:
		err = s.scanLog(strings.Fields(opts.rng)...)
	case opts.tag:
		err = s.scanTag(opts.paths[0], opts.paths[1])
	case opts.names:
		for _, n := range opts.paths {
			s.scanName(n)
		}
	case opts.history:
		err = s.scanLog("HEAD")
	default:
		err = s.scanPaths(opts.paths)
	}

	if err != nil {
		_, _ = fmt.Fprintf(stderr, "namecheck: %v\n", err)

		return 2
	}

	if s.failed {
		return 1
	}

	return 0
}
