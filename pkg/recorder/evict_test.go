package recorder

import (
	"testing"

	tesserav1 "github.com/KeshavGoyal26/tessera/gen/go/tessera/v1"
)

func TestReapForgetsOldestFinishedJobs(t *testing.T) {
	c := NewCoordinator(CoordinatorParams{})
	defer c.Close()
	c.mu.Lock()
	for i := range maxFinishedJobs + 10 {
		id := newRecordingID()
		st := tesserav1.RecordingStatus_RECORDING_STATUS_COMPLETE
		if i == 0 {
			st = tesserav1.RecordingStatus_RECORDING_STATUS_ACTIVE // never evicted
		}
		c.jobs[id] = &job{info: &tesserav1.RecordingInfo{RecordingId: id, Status: st}}
		c.order = append(c.order, id)
	}
	first := c.order[0]
	c.mu.Unlock()
	c.Reap()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.jobs) != maxFinishedJobs+1 || len(c.order) != maxFinishedJobs+1 {
		t.Fatalf("kept %d jobs (%d ordered), want %d", len(c.jobs), len(c.order), maxFinishedJobs+1)
	}
	if c.jobs[first] == nil {
		t.Fatal("active job was evicted")
	}
}
