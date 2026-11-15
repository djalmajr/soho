package setuptext

import (
	"strings"
	"testing"
)

func TestPiNativePeerPrefilter(t *testing.T) {
	text := PiInboundExtension()
	const condition = `if (typeof event.text !== "string" || (!event.text.includes("[soho:peer") && !event.text.includes('<hc from="'))) {`
	if !strings.Contains(text, condition) || !strings.Contains(text, `const result = await checkPeer(event.text);`) {
		t.Fatal("native and soho inputs must reach the hook through the substring prefilter")
	}
}
