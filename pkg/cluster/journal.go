package cluster

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/KeshavGoyal26/tessera/pkg/recorder"
)

const journalLockTTL = 2 * time.Minute

// Journal is the recorder's crash journal in Redis (recorder.JournalStore): with
// shared storage (S3) any media node closes the recordings of a node that is gone,
// even when that machine never comes back (ADR-0012). An entry is orphaned when its
// node has left the registry, or when it is this node's but not running here (the
// node restarted).
//
//	journal:<id>       hash: owner, job, t:<track sid> (one per track)
//	journals           set of recording ids
//	journal-lock:<id>  the node closing an orphan
type Journal struct {
	C    *Client
	Node string

	mu     sync.Mutex
	active map[string]bool
}

var _ recorder.JournalStore = (*Journal)(nil)

func journalKey(id string) string { return keyPrefix + "journal:" + id }

// Begin implements recorder.JournalStore.
func (j *Journal) Begin(id string, job []byte) (string, error) {
	j.mu.Lock()
	if j.active == nil {
		j.active = map[string]bool{}
	}
	j.active[id] = true
	j.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := j.C.rdb.TxPipelined(ctx, func(p redis.Pipeliner) error {
		p.HSet(ctx, journalKey(id), "owner", j.Node, "job", job)
		p.SAdd(ctx, keyPrefix+"journals", id)
		return nil
	})
	if err != nil {
		j.End(id)
		return "", err
	}
	return id, nil
}

// Put implements recorder.JournalStore.
func (j *Journal) Put(handle, track string, data []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return j.C.rdb.HSet(ctx, journalKey(handle), "t:"+track, data).Err()
}

// End implements recorder.JournalStore.
func (j *Journal) End(handle string) {
	j.mu.Lock()
	delete(j.active, handle)
	j.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	j.remove(ctx, handle)
}

func (j *Journal) remove(ctx context.Context, id string) {
	_, _ = j.C.rdb.TxPipelined(ctx, func(p redis.Pipeliner) error {
		p.Del(ctx, journalKey(id), keyPrefix+"journal-lock:"+id)
		p.SRem(ctx, keyPrefix+"journals", id)
		return nil
	})
}

// Orphans implements recorder.JournalStore.
func (j *Journal) Orphans(ctx context.Context) ([]recorder.JournalEntry, error) {
	ids, err := j.C.rdb.SMembers(ctx, keyPrefix+"journals").Result()
	if err != nil {
		return nil, err
	}
	var out []recorder.JournalEntry
	for _, id := range ids {
		h, err := j.C.rdb.HGetAll(ctx, journalKey(id)).Result()
		if err != nil {
			return out, err
		}
		switch owner := h["owner"]; owner {
		case "":
			_ = j.C.rdb.SRem(ctx, keyPrefix+"journals", id).Err() // already removed
			continue
		case j.Node:
			j.mu.Lock()
			running := j.active[id]
			j.mu.Unlock()
			if running {
				continue
			}
		default:
			alive, err := j.C.rdb.Exists(ctx, nodeKey(owner)).Result()
			if err != nil || alive > 0 {
				continue
			}
		}
		ok, err := j.C.rdb.SetNX(ctx, keyPrefix+"journal-lock:"+id, j.Node, journalLockTTL).Result()
		if err != nil || !ok {
			continue // another node is closing it
		}
		e := recorder.JournalEntry{ID: id, Job: []byte(h["job"])}
		for k, v := range h {
			if strings.HasPrefix(k, "t:") {
				e.Tracks = append(e.Tracks, []byte(v))
			}
		}
		e.Done = func() {
			dctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			j.remove(dctx, id)
		}
		out = append(out, e)
	}
	return out, nil
}
