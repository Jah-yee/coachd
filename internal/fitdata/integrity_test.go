package fitdata

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/muktihari/fit/kit/hash/crc16"
)

func crc(b []byte) uint16 {
	h := crc16.New()
	h.Write(b)
	return h.Sum16()
}

// withFileCRC sets b's file CRC over its header and data records, as the
// FIT SDK computes it.
func withFileCRC(b []byte) []byte {
	end := len(b) - 2
	binary.LittleEndian.PutUint16(b[end:], crc(b[:end]))
	return b
}

// zeroHeaderCRC is b as Zwift writes some files: a 14-byte header whose
// optional CRC is 0x0000, and the file CRC over header and data.
func zeroHeaderCRC(b []byte) []byte {
	out := bytes.Clone(b)
	out[12], out[13] = 0, 0
	return withFileCRC(out)
}

func TestDecodeZeroHeaderCRC(t *testing.T) {
	b := build(t, 2, 120, 140, 3.0)
	if b[0] != 14 || binary.LittleEndian.Uint16(b[12:14]) == 0 {
		t.Fatalf("fixture header: size %d, crc %x", b[0], b[12:14])
	}
	want, err := Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(bytes.NewReader(zeroHeaderCRC(b)))
	if err != nil {
		t.Fatalf("header CRC 0x0000: %v", err)
	}
	if len(got.Laps) != len(want.Laps) || len(got.Points) != len(want.Points) {
		t.Fatalf("laps %d points %d, want %d and %d", len(got.Laps), len(got.Points), len(want.Laps), len(want.Points))
	}
}

func TestDecodeRejectsDamagedFiles(t *testing.T) {
	b := build(t, 2, 120, 140, 3.0)
	flip := func(b []byte, i int) []byte {
		out := bytes.Clone(b)
		out[i] ^= 0x01
		return out
	}
	for name, in := range map[string][]byte{
		"data byte":                    flip(b, len(b)/2),
		"data byte, header CRC 0x0000": flip(zeroHeaderCRC(b), len(b)/2),
		"file CRC":                     flip(b, len(b)-1),
		"header CRC":                   withFileCRC(flip(b, 12)),
		"data size, header CRC 0x0000": flip(zeroHeaderCRC(b), 4),
		"truncated":                    b[:len(b)-3],
		"truncated, header CRC 0x0000": zeroHeaderCRC(b)[:len(b)-3],
		"header only":                  b[:14],
		"not a FIT file":               []byte("not a FIT"),
	} {
		_, err := Decode(bytes.NewReader(in))
		if err == nil {
			t.Errorf("%s: decoded", name)
		}
		if _, err := DecodeWorkout(bytes.NewReader(in)); err == nil {
			t.Errorf("%s: DecodeWorkout decoded", name)
		}
	}
	if _, err := Decode(errReader{}); err == nil || !errors.Is(err, errRead) {
		t.Errorf("read error: %v", err)
	}
}

var errRead = errors.New("read failed")

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errRead }
