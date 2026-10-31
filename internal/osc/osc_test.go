package osc

import (
	"encoding/base64"
	"strings"
	"testing"
)

func seq(body, term string) string { return "\x1b]7501;" + body + term }

func TestScannerFindsSequences(t *testing.T) {
	msg := base64.StdEncoding.EncodeToString([]byte("Apply 3 to add, 1 to change, 0 to destroy?"))
	stream := "hello \x1b[1mbold\x1b[0m " +
		seq("state=blocked:kind=permission:app=terraform:msg="+msg, "\x1b\\") +
		" more output " +
		seq("state=working:progress=40", "\a") +
		"\x1b]0;window title\a" +
		seq("nostate=1", "\x1b\\") +
		seq("state=done", "\x1b\\")

	var s Scanner
	got := s.Write([]byte(stream))
	if len(got) != 3 {
		t.Fatalf("got %d statuses: %+v", len(got), got)
	}
	if got[0].State != "blocked" || got[0].Kind != "permission" || got[0].App != "terraform" ||
		got[0].Msg != "Apply 3 to add, 1 to change, 0 to destroy?" {
		t.Errorf("first: %+v", got[0])
	}
	if got[1].State != "working" || got[1].Progress != "40" {
		t.Errorf("second: %+v", got[1])
	}
	if got[2].State != "done" {
		t.Errorf("third: %+v", got[2])
	}
}

func TestScannerHandlesSplitReads(t *testing.T) {
	stream := "abc" + seq("state=working:msg="+base64.StdEncoding.EncodeToString([]byte("Syncing photos")), "\x1b\\") + "xyz"
	// Feed one byte at a time: every split point must work.
	var s Scanner
	var got []Status
	for i := 0; i < len(stream); i++ {
		got = append(got, s.Write([]byte{stream[i]})...)
	}
	if len(got) != 1 || got[0].Msg != "Syncing photos" {
		t.Fatalf("got %+v", got)
	}
}

func TestScannerRecoversFromCutSequence(t *testing.T) {
	var s Scanner
	// An unterminated sequence followed by a new one.
	got := s.Write([]byte("\x1b]7501;state=work\x1b]7501;state=idle\x1b\\"))
	if len(got) != 1 || got[0].State != "idle" {
		t.Fatalf("got %+v", got)
	}
	// An oversized body is dropped and scanning continues.
	got = s.Write([]byte("\x1b]7501;state=" + strings.Repeat("x", maxBody+10) + "\x1b\\" + seq("state=done", "\a")))
	if len(got) != 1 || got[0].State != "done" {
		t.Fatalf("after oversize: %+v", got)
	}
}

func TestMsgIsSanitized(t *testing.T) {
	msg := base64.StdEncoding.EncodeToString([]byte("line one\nline\x1b[31m two"))
	st, ok := Parse("state=error:msg=" + msg)
	if !ok || strings.ContainsAny(st.Msg, "\n\x1b") || st.Msg != "line one line [31m two" {
		t.Fatalf("got %q", st.Msg)
	}
	// Not base64: kept as text.
	if st, _ := Parse("state=working:msg=plain words"); st.Msg != "plain words" {
		t.Fatalf("plain msg: %q", st.Msg)
	}
}
