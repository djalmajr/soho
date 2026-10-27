package job

import "testing"

func TestReportEnvItems(t *testing.T) {
	items := reportItems("- [env] no ports; host: go test ./...\n- [env]\n```\n- [env] fenced reason\n```\n- [env] reason [done]\n- [env] [env] repeated\n- [done] delivered\n")
	if len(items) != 3 || items[0].Estado != "env" || items[1].Estado != "partial" || items[2].Estado != "done" {
		t.Fatalf("items=%+v", items)
	}
	t.Logf("items=%+v", items)
}
