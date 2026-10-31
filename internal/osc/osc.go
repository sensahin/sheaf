// Package osc reads OSC 7501 program status sequences out of a terminal byte
// stream.
//
// A program reports its status by printing
//
//	ESC ] 7501 ; state=blocked:kind=permission:app=claude-code:msg=<base64> ESC \
//
// (BEL is accepted as the terminator too). The scanner sees every byte the
// agent writes, passes nothing through itself, and reports each complete
// sequence it finds, even when one is split across reads.
package osc

import (
	"encoding/base64"
	"strings"
)

// Status is one OSC 7501 report.
type Status struct {
	State    string // idle, working, done, blocked, error, or clear
	Kind     string // for blocked: permission, question or auth
	App      string
	Msg      string // decoded
	Progress string
	ID       string
	// Fields holds every key, raw, for keys this package does not name.
	Fields map[string]string
}

// maxBody bounds a sequence so a stray ESC ] 7501 cannot make the scanner
// buffer the rest of the stream.
const maxBody = 4096

const (
	esc = 0x1b
	bel = 0x07
)

var prefix = []byte("\x1b]7501;")

// Scanner finds status sequences in a stream fed to it in chunks.
type Scanner struct {
	matched int    // bytes of prefix matched so far
	inBody  bool   // inside a sequence body
	sawEsc  bool   // the last body byte was ESC (maybe the start of ST)
	body    []byte // the body so far
}

// Write scans p and returns the statuses completed in it.
func (s *Scanner) Write(p []byte) []Status {
	var out []Status
	for _, b := range p {
		if s.inBody {
			switch {
			case s.sawEsc && b == '\\':
				out = s.finish(out)
			case s.sawEsc:
				// ESC not followed by '\': the sequence was cut off. Start
				// over, treating this ESC as a possible new introducer.
				s.reset()
				s.matched = 1
				s.feedPrefix(b)
			case b == esc:
				s.sawEsc = true
			case b == bel:
				out = s.finish(out)
			default:
				if len(s.body) >= maxBody {
					s.reset()
					continue
				}
				s.body = append(s.body, b)
			}
			continue
		}
		s.feedPrefix(b)
	}
	return out
}

func (s *Scanner) feedPrefix(b byte) {
	if b == prefix[s.matched] {
		s.matched++
		if s.matched == len(prefix) {
			s.matched = 0
			s.inBody = true
			s.body = s.body[:0]
		}
		return
	}
	s.matched = 0
	if b == prefix[0] {
		s.matched = 1
	}
}

func (s *Scanner) finish(out []Status) []Status {
	if st, ok := Parse(string(s.body)); ok {
		out = append(out, st)
	}
	s.reset()
	return out
}

func (s *Scanner) reset() {
	s.inBody, s.sawEsc, s.matched = false, false, 0
	s.body = s.body[:0]
}

// Parse reads a sequence body such as "state=working:msg=SGk=". It reports
// false when the body has no state.
func Parse(body string) (Status, bool) {
	st := Status{Fields: map[string]string{}}
	for _, kv := range strings.Split(strings.TrimSpace(body), ":") {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		st.Fields[k] = v
		switch k {
		case "state":
			st.State = v
		case "kind":
			st.Kind = v
		case "app":
			st.App = v
		case "progress":
			st.Progress = v
		case "id":
			st.ID = v
		case "msg":
			st.Msg = decode(v)
		}
	}
	return st, st.State != ""
}

func decode(v string) string {
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(v); err == nil {
			return oneLine(string(b))
		}
	}
	return oneLine(v)
}

func oneLine(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}
