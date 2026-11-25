package api

import (
	"ccgsc/poolscore/internal/pool"
	"testing"
)

func TestRunAuditF4ResultsPageAnchor(t *testing.T) {
	j := pool.Job{ChainMode: pool.ChainModePerRun, ChainHead: "head", ChainEntries: 3, AuditGap: true}
	view := resultPageView(j, []any{}, false, "")
	if view["chainHead"] != "head" || view["chainEntries"] != 3 || view["auditGap"] != true {
		t.Fatalf("missing evidence: %+v", view)
	}
	j.ChainMode = pool.ChainModeNone
	if _, ok := resultPageView(j, []any{}, false, "")["chainHead"]; ok {
		t.Fatal("legacy mode gained a chain anchor")
	}
}
