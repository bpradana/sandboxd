package agent

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestFrameRoundTrip(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	conn := NewConn(&buf)
	want := &Frame{Type: TypeExec, Exec: &ExecSpec{Argv: []string{"echo", "hi"}, Env: []string{"A=1"}, TimeoutMs: 1000, Stdin: []byte{0, 1, 2}}}
	if err := conn.Write(want); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	got, err := conn.Read()
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if got.Type != TypeExec || got.Exec == nil || got.Exec.Argv[1] != "hi" || !bytes.Equal(got.Exec.Stdin, []byte{0, 1, 2}) {
		t.Fatalf("Read() = %+v", got)
	}
}

func TestReadRejectsOversizedFrame(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], MaxFrameSize+1)
	buf.Write(header[:])

	if _, err := NewConn(&buf).Read(); err == nil {
		t.Fatal("Read() accepted an oversized frame")
	}
}

func TestReadRejectsUntypedFrame(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	payload := []byte(`{"data":"aGk="}`)
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(payload)))
	buf.Write(header[:])
	buf.Write(payload)

	if _, err := NewConn(&buf).Read(); err == nil {
		t.Fatal("Read() accepted a frame without type")
	}
}

func TestTTYFramesRoundTrip(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	conn := NewConn(&buf)
	frames := []*Frame{
		{Type: TypeTTYOpen, TTY: &TTYSpec{Argv: []string{"/bin/sh", "-l"}, Env: []string{"TERM=xterm-256color"}, Cwd: "/home/user", Cols: 120, Rows: 40}},
		{Type: TypeStdin, Data: []byte("ls\r")},
		{Type: TypeResize, TTY: &TTYSpec{Cols: 80, Rows: 24}},
	}
	for _, frame := range frames {
		if err := conn.Write(frame); err != nil {
			t.Fatalf("Write(%s) error = %v", frame.Type, err)
		}
	}

	open, err := conn.Read()
	if err != nil || open.Type != TypeTTYOpen || open.TTY == nil || open.TTY.Cols != 120 || open.TTY.Rows != 40 || open.TTY.Argv[0] != "/bin/sh" {
		t.Fatalf("Read() open = %+v, %v", open, err)
	}
	stdin, err := conn.Read()
	if err != nil || stdin.Type != TypeStdin || string(stdin.Data) != "ls\r" {
		t.Fatalf("Read() stdin = %+v, %v", stdin, err)
	}
	resize, err := conn.Read()
	if err != nil || resize.Type != TypeResize || resize.TTY == nil || resize.TTY.Cols != 80 || resize.TTY.Rows != 24 {
		t.Fatalf("Read() resize = %+v, %v", resize, err)
	}
}
