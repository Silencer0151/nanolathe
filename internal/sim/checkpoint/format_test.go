package checkpoint

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"math"
	"strings"
	"testing"
)

func authoredIdentity() Identity {
	var identity Identity
	for i := range identity.Content {
		identity.Content[i] = byte(i)
		identity.Config[i] = byte(i + 32)
	}
	return identity
}

func writeAuthoredCapture(t *testing.T, identity Identity, out io.Writer, changed bool) Digests {
	t.Helper()
	c, err := NewCapture(identity, out)
	if err != nil {
		t.Fatal(err)
	}
	// Named constants in published order also lock each exported ID against
	// the independently authored byte and digest vectors below.
	owners := [...]Owner{
		OwnerRuntime, OwnerUnits, OwnerOrders, OwnerScripts, OwnerWorld,
		OwnerVisibility, OwnerMovement, OwnerPaths, OwnerEconomy,
		OwnerConstruction, OwnerCombat, OwnerEffects, OwnerComputersScenario,
	}
	for _, owner := range owners {
		e, err := c.Section(owner, owner == OwnerRuntime || owner == OwnerWorld)
		if err != nil {
			t.Fatal(err)
		}
		switch owner {
		case OwnerRuntime:
			e.Field("authored.tick")
			e.U32(0x01020304)
			e.Bool(true)
			e.String("entry")
		case OwnerWorld:
			e.Field("authored.rows")
			e.Count(2)
			if changed {
				e.String("ROW")
			} else {
				e.String("row")
			}
			e.Bytes(nil)
		}
	}
	digests, err := c.Finish()
	if err != nil {
		t.Fatal(err)
	}
	return digests
}

func TestCaptureAuthoredByteAndHashVectors(t *testing.T) {
	// The stream is authored independently of Encoder. Digest vectors were
	// computed with Python hashlib over these bytes and the specified domains.
	wantBytes := "4e4c4350535441540600" +
		"000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f" +
		"202122232425262728292a2b2c2d2e2f303132333435363738393a3b3c3d3e3f" +
		"0d00" +
		"010001040302010105000000656e747279" +
		"020000030000040000" +
		"0500010200000003000000726f7700000000" +
		"0600000700000800000900000a00000b00000c00000d0000"
	const wantFull = "7327cee3b31d62e59a7a6776d5e7b0032d405ab9636021120ff90db889196e99"
	wantOwners := [OwnerCount]string{
		"eeada693db3b46ab67d990a0ff13f5801dae19d0589f44f84dd4e9e1e2c747d5",
		"1be4546b832f7a0e5032d1b8e9ac1198a50f97e22d0f41358db4a4874012aa83",
		"cbd3dc3106a38a901a01aea00c0decae0f294271cd852398eacde07b088b12b7",
		"4917531941062c9d034a2f58a8af5e0c0ee21f175abafcf6abc0244119c875ed",
		"2ec42a8de198d198d8704f960213ed79f2244795e79ea84133a50aafc59c1b2f",
		"0a51e703ebee6ff8b0bf2858d622111e70993a2face5812424cbac656313c02b",
		"aabfdb848d8245cf2bee1d4283befd8e74b720aaf06b7a637c27374b2e290766",
		"a610040ab8074b476c847ad2c37c53a89d2d785ec9d7ade1eea5f474cb45bc0c",
		"b72f3695b170897273c44575325d3ebca7170f3db2d98c0f33e9d1e7536ede1d",
		"ab3543e9a8a7bf9696f0008ae06bc04aec4339442f70f125a86409dd8ef42a39",
		"33c8e70618eb8760aeea7bcce44a6187f93cc43fa694da5925de8c92d669f744",
		"4c68dcd33cbc78a4bdb7b39fe7c4be973dda0830b009cf5c47281ffef5a7713f",
		"61109db6f6d2f807bc18138ebd29ae272d1e51c37042bff4d40b55ebf33ad560",
	}
	var out bytes.Buffer
	got := writeAuthoredCapture(t, authoredIdentity(), &out, false)
	if hex.EncodeToString(out.Bytes()) != wantBytes {
		t.Fatalf("stream = %x, want %s", out.Bytes(), wantBytes)
	}
	if hex.EncodeToString(got.Full[:]) != wantFull {
		t.Fatalf("full = %x, want %s", got.Full, wantFull)
	}
	for i, owner := range got.Owners {
		if hex.EncodeToString(owner[:]) != wantOwners[i] {
			t.Errorf("owner %d = %x, want %s", i+1, owner, wantOwners[i])
		}
	}
	if got.Full != sha256.Sum256(out.Bytes()) {
		t.Fatal("diagnostic bytes and full digest disagree")
	}
	if hashOnly := writeAuthoredCapture(t, authoredIdentity(), nil, false); got != hashOnly {
		t.Fatal("hash-only capture differs from diagnostic byte capture")
	}
}

func TestCaptureOwnerDomainsAndIdentity(t *testing.T) {
	base := writeAuthoredCapture(t, authoredIdentity(), nil, false)
	changed := writeAuthoredCapture(t, authoredIdentity(), nil, true)
	if base.Full == changed.Full {
		t.Fatal("changed payload did not change full digest")
	}
	for i := range base.Owners {
		if equal := base.Owners[i] == changed.Owners[i]; equal == (i == int(OwnerWorld)-1) {
			t.Fatalf("owner %d isolated payload change incorrectly: equal %t", i+1, equal)
		}
	}
	for _, config := range []bool{false, true} {
		identity := authoredIdentity()
		if config {
			identity.Config[3] ^= 1
		} else {
			identity.Content[3] ^= 1
		}
		bound := writeAuthoredCapture(t, identity, nil, false)
		if base.Full == bound.Full {
			t.Fatal("identity did not change full digest")
		}
		for i := range base.Owners {
			if base.Owners[i] == bound.Owners[i] {
				t.Fatalf("identity did not change owner %d digest", i+1)
			}
		}
	}
}

func TestCaptureSectionOrderAndCompleteness(t *testing.T) {
	tests := []struct {
		name   string
		owners []Owner
	}{
		{"none", nil},
		{"missing", []Owner{OwnerRuntime}},
		{"repeat", []Owner{OwnerRuntime, OwnerRuntime}},
		{"skip", []Owner{OwnerRuntime, OwnerOrders}},
		{"backwards", []Owner{OwnerUnits}},
		{"zero", []Owner{0}},
		{"unknown", []Owner{14}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := NewCapture(Identity{}, nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, owner := range tt.owners {
				_, err = c.Section(owner, false)
				if err != nil {
					break
				}
			}
			digests, finishErr := c.Finish()
			if finishErr == nil || digests != (Digests{}) {
				t.Fatalf("invalid section sequence accepted: %v, %v", digests, finishErr)
			}
			if _, err := c.Section(OwnerRuntime, false); err != finishErr {
				t.Fatalf("section error changed: %v, want %v", err, finishErr)
			}
		})
	}
}

func TestCaptureClosedAndAbsentSections(t *testing.T) {
	for _, misuse := range []string{"absent", "advanced", "copied advanced", "finished", "copied finished", "finish twice", "section after finish"} {
		t.Run(misuse, func(t *testing.T) {
			var out bytes.Buffer
			c, err := NewCapture(Identity{}, &out)
			if err != nil {
				t.Fatal(err)
			}
			first, err := c.Section(OwnerRuntime, misuse != "absent")
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(misuse, "copied") {
				copied := *first
				first = &copied
			}
			if misuse != "absent" {
				for owner := OwnerUnits; owner <= OwnerComputersScenario; owner++ {
					if _, err := c.Section(owner, false); err != nil {
						t.Fatal(err)
					}
				}
			}
			if misuse == "finished" || misuse == "copied finished" || misuse == "finish twice" || misuse == "section after finish" {
				if _, err := c.Finish(); err != nil {
					t.Fatal(err)
				}
			}
			before := out.Len()
			if misuse == "section after finish" {
				if _, err := c.Section(OwnerRuntime, true); err == nil {
					t.Fatal("section after finish accepted")
				}
			} else if misuse != "finish twice" {
				first.Field("late.payload")
				first.Bytes(nil)
				if first.Err() == nil || !strings.Contains(first.Err().Error(), "runtime") || !strings.Contains(first.Err().Error(), "late.payload") {
					t.Fatalf("missing section misuse context: %v", first.Err())
				}
			}
			if got, err := c.Finish(); err == nil || got != (Digests{}) {
				t.Fatalf("misuse retained usable digests: %v, %v", got, err)
			}
			if out.Len() != before {
				t.Fatal("misuse wrote bytes")
			}
		})
	}
}

func TestCaptureFailuresInvalidateAllDigests(t *testing.T) {
	const headerSize = 8 + 2 + 32 + 32 + 2
	cause := errors.New("capture sink failure")
	for _, allowance := range []int{0, 9, headerSize, headerSize + 2, headerSize + 5} {
		for _, sinkErr := range []error{nil, cause} {
			w := &faultSink{remaining: allowance, err: sinkErr}
			c, err := NewCapture(Identity{}, w)
			want := sinkErr
			if want == nil {
				want = io.ErrShortWrite
			}
			if err == nil {
				e, sectionErr := c.Section(OwnerRuntime, true)
				if sectionErr == nil {
					e.Field("authored.tick")
					e.U32(7)
				}
				calls := w.calls
				var got Digests
				got, err = c.Finish()
				if got != (Digests{}) || calls != w.calls {
					t.Fatal("failed sink produced digests or accepted more writes")
				}
			} else if c != nil {
				t.Fatal("header failure returned a capture")
			}
			if !errors.Is(err, want) {
				t.Fatalf("allowance %d: %v, want %v", allowance, err, want)
			}
		}
	}
	var out bytes.Buffer
	c, err := NewCapture(Identity{}, &out)
	if err != nil {
		t.Fatal(err)
	}
	e, err := c.Section(OwnerRuntime, true)
	if err != nil {
		t.Fatal(err)
	}
	e.Field("wind.scalar")
	e.F32(math.Float32frombits(0x7fc00001))
	before := out.Len()
	if got, err := c.Finish(); err == nil || got != (Digests{}) || !strings.Contains(err.Error(), "runtime") || !strings.Contains(err.Error(), "wind.scalar") {
		t.Fatalf("NaN did not invalidate capture with context: %v, %v", got, err)
	}
	e.U8(1)
	if out.Len() != before {
		t.Fatal("NaN allowed a later payload")
	}
}

func TestCaptureHashBuffersPreserveSectionBytes(t *testing.T) {
	var out, expected bytes.Buffer
	identity := authoredIdentity()
	c, err := NewCapture(identity, &out)
	if err != nil {
		t.Fatal(err)
	}
	expected.WriteString("NLCPSTAT")
	expected.Write([]byte{6, 0})
	expected.Write(identity.Content[:])
	expected.Write(identity.Config[:])
	expected.Write([]byte{13, 0})
	var want Digests
	for owner := OwnerRuntime; owner <= OwnerComputersScenario; owner++ {
		e, err := c.Section(owner, true)
		if err != nil {
			t.Fatal(err)
		}
		var section bytes.Buffer
		section.Write([]byte{byte(owner), 0, 1})
		// Sizes on both sides of the hash buffer boundary, in one-byte writes.
		for i := 0; i < 4090+int(owner); i++ {
			value := byte(i*7 + int(owner))
			e.U8(value)
			section.WriteByte(value)
		}
		expected.Write(section.Bytes())
		var domain bytes.Buffer
		domain.WriteString("NLCPSECT")
		domain.Write([]byte{6, 0})
		domain.Write(identity.Content[:])
		domain.Write(identity.Config[:])
		domain.Write(section.Bytes())
		want.Owners[owner-1] = sha256.Sum256(domain.Bytes())
	}
	want.Full = sha256.Sum256(expected.Bytes())
	got, err := c.Finish()
	if err != nil || got != want || !bytes.Equal(out.Bytes(), expected.Bytes()) {
		t.Fatalf("buffered canonical digest/bytes differ: %v", err)
	}
}
