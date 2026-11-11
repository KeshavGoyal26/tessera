package config

import "testing"

// The example deployment configs must load and validate (unknown keys are rejected),
// so they cannot drift from the code.
func TestExampleDeployConfigLoads(t *testing.T) {
	t.Setenv("TESSERA_DEV_SECRET", "devsecret-devsecret-devsecret-0000")
	t.Setenv("TESSERA_NODE_IP", "203.0.113.10")
	c, err := Load("../../deploy/config/tessera.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !c.Recording.Enabled || c.Recording.Storage.Kind != "local" || c.Recording.Storage.LocalBaseURL != "/recordings" {
		t.Fatalf("recording %+v", c.Recording)
	}
}

func TestExampleClusterNodeConfigLoads(t *testing.T) {
	t.Setenv("TESSERA_DEV_SECRET", "devsecret-devsecret-devsecret-0000")
	t.Setenv("TESSERA_NODE_IP", "203.0.113.10")
	for _, n := range []struct{ id, role, rec, port, recPort string }{
		{"m1", RoleMedia, "true", "7910", "6791"},
		{"e1", RoleEdge, "false", "7930", "0"},
	} {
		t.Setenv("NODE_ID", n.id)
		t.Setenv("ROLE", n.role)
		t.Setenv("RECORDING", n.rec)
		t.Setenv("PORT", n.port)
		t.Setenv("TCP_PORT", "7911")
		t.Setenv("UDP_PORT", "7912")
		t.Setenv("REC_PORT", n.recPort)
		c, err := Load("../../deploy/config/cluster-node.yaml")
		if err != nil {
			t.Fatalf("%s: %v", n.id, err)
		}
		if c.Cluster.Role != n.role || c.Recording.Enabled != (n.rec == "true") || c.Cluster.PrivateURL != "http://"+n.id+":"+n.port {
			t.Fatalf("%s: cluster %+v recording %+v", n.id, c.Cluster, c.Recording)
		}
	}
}
