// T42 F1, 2026-11-18: HTTP split, batching and capability contract.
package tests

import (
	"encoding/json"
	"fmt"
	"testing"

	"ccgsc/poolscore/internal/pool"
	"ccgsc/poolscore/internal/verify"
)

func TestF1HTTPSplitBatchCapabilities(t *testing.T) {
	for _, base := range []string{"/thread-pool/v1", "/process-pool/v1"} {
		t.Run(base, func(t *testing.T) {
			f := setup(t)
			_, dev := f.key(t, "developer")
			_, contributor := f.key(t, "contributor")
			module := f.artifact(t, base, dev)
			capabilities := decode[struct {
				Limits struct {
					MaxTasks, MaxBatch int `json:"-"`
					Tasks              int `json:"maxTasksPerJob"`
					Batch              int `json:"maxBatchSize"`
				} `json:"serverLimits"`
				GPU struct {
					Supported bool `json:"supported"`
				} `json:"gpu"`
				PriorityClasses []string `json:"priorityClasses"`
				Limitations     []string `json:"limitations"`
			}](t, f.request(t, "GET", base+"/capabilities", dev, nil, 200, nil))
			if capabilities.Limits.Tasks != 10000 || capabilities.Limits.Batch != 10000 ||
				len(capabilities.PriorityClasses) != 3 || !capabilities.GPU.Supported {
				t.Fatalf("untruthful capabilities: %+v", capabilities)
			}
			for _, s := range capabilities.Limitations {
				if s == "split helpers" {
					t.Fatal("split still listed as unavailable")
				}
			}
			data := []byte(`{"start":0,"end":10}`)
			id := verify.SHA256(data)
			f.request(t, "PUT", base+"/artifacts", dev, data, 201, map[string]string{"X-Artifact-Sha256": id, "Upload-Offset": "0", "Upload-Length": fmt.Sprint(len(data))})
			zero := 0.0
			q := pool.CreateRequest{ModuleArtifactIDs: []string{module}, Split: &pool.SplitSpec{ArtifactID: id, Format: "range", Count: 3, Chunksize: 4}, BatchSize: 2, RerunPercent: &zero}
			j := decode[pool.Job](t, f.request(t, "POST", base+"/jobs", dev, q, 201, nil))
			w := decode[pool.Worker](t, f.request(t, "POST", base+"/workers/register", contributor, map[string]any{"hostId": "f1", "slots": 2}, 201, nil))
			for _, size := range []int{2, 1} {
				a := decode[pool.Assignment](t, f.request(t, "POST", base+"/workers/"+w.TabID+"/leases", contributor, nil, 200, nil))
				if len(a.Batch) != size {
					t.Fatalf("got %d members, want %d", len(a.Batch), size)
				}
				for _, m := range a.Batch {
					var input pool.SplitInput
					if err := json.Unmarshal(m.Task.Input, &input); err != nil || input.Range == nil {
						t.Fatalf("range: %v", err)
					}
					output := json.RawMessage(`{"ok":true}`)
					f.request(t, "POST", base+"/workers/"+w.TabID+"/results", contributor, pool.Submission{Lease: m.Lease, TaskEnvelope: m.TaskEnvelope, LeaseReceipt: m.LeaseReceipt, Output: output, SHA256: verify.SHA256(output)}, 200, nil)
				}
			}
			got := decode[pool.Job](t, f.request(t, "GET", base+"/jobs/"+j.JobID, dev, nil, 200, nil))
			if got.State != "completed" || got.Progress.Completed != 3 {
				t.Fatalf("completion: %+v", got.Progress)
			}
		})
	}
}
