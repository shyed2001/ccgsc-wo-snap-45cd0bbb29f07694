package grpcapi

import (
	"ccgsc/poolscore/internal/pool"
	"testing"
)

func TestRunAuditF4GRPCFields(t *testing.T) {
	report := pool.ChainReport{JobID: "job", Mode: pool.ChainModePerRun, Head: "head", AuditGap: true,
		RunEntries: []pool.RunAuditEntry{{Seq: 1, Kind: "module", Digest: "digest", Hash: "head"}}}
	view := chainReportView(report)
	if !view.GetAuditGap() || len(view.GetRunEntries()) != 1 || view.RunEntries[0].GetSha256() != "digest" || view.RunEntries[0].GetKind() != "module" {
		t.Fatalf("run entries dropped in grpc: %+v", view)
	}
	j := jobView(pool.Job{ChainMode: pool.ChainModePerRun, ChainHead: "head", ChainEntries: 1, AuditGap: true}, 0)
	if !j.GetAuditGap() || j.GetChainHead() != "head" || j.GetChainEntries() != 1 {
		t.Fatal("job audit fields missing")
	}
}
