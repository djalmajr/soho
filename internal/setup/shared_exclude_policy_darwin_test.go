//go:build darwin

package setup

import (
	"encoding/binary"
	"testing"
)

// The parser must distinguish a plain file (zero extended-security bytes)
// from a named ACL and reject an unmeasurable reference rather than treating
// it as absence. Both shapes come from the native fgetattrlist receipt.
func TestDarwinACLResponseDecision(t *testing.T) {
	plain := make([]byte, 12)
	binary.LittleEndian.PutUint32(plain[0:4], 12)
	binary.LittleEndian.PutUint32(plain[4:8], 8)
	got, err := parseDarwinACLResponse(plain)
	if err != nil || len(got) != 0 {
		t.Fatalf("plain response -> %q, %v; want no ACL and no error", got, err)
	}

	acl := []byte("named-acl-entry")
	response := make([]byte, 12+len(acl))
	binary.LittleEndian.PutUint32(response[0:4], uint32(len(response)))
	binary.LittleEndian.PutUint32(response[4:8], 8)
	binary.LittleEndian.PutUint32(response[8:12], uint32(len(acl)))
	copy(response[12:], acl)
	got, err = parseDarwinACLResponse(response)
	if err != nil || string(got) != string(acl) {
		t.Fatalf("ACL response -> %q, %v; want %q", got, err, acl)
	}

	for _, tc := range []struct {
		name string
		resp []byte
	}{
		{"short", make([]byte, 8)},
		{"bad offset", func() []byte {
			b := append([]byte(nil), response...)
			binary.LittleEndian.PutUint32(b[4:8], 4)
			return b
		}()},
		{"overflowing length", func() []byte {
			b := append([]byte(nil), response...)
			binary.LittleEndian.PutUint32(b[8:12], uint32(len(response)))
			return b
		}()},
		{"zero length with trailing data", func() []byte {
			b := append([]byte(nil), response...)
			binary.LittleEndian.PutUint32(b[8:12], 0)
			return b
		}()},
		{"truncated reported response", func() []byte {
			return response[:len(response)-1]
		}()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := parseDarwinACLResponse(tc.resp); err == nil {
				t.Fatalf("unmeasurable response accepted as %q", got)
			}
		})
	}
}
