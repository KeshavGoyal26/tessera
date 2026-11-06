package config

import "testing"

// The example deployment config must load and validate (unknown keys are rejected),
// so it cannot drift from the code.
func TestExampleDeployConfigLoads(t *testing.T) {
	t.Setenv("TESSERA_DEV_SECRET", "devsecret-devsecret-devsecret-0000")
	t.Setenv("TESSERA_NODE_IP", "203.0.113.10")
	t.Setenv("MINIO_ROOT_USER", "minio")
	t.Setenv("MINIO_ROOT_PASSWORD", "minio-password")
	c, err := Load("../../deploy/config/tessera.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !c.Recording.Enabled || c.Recording.Storage.Kind != "s3" || c.Recording.Storage.S3.Endpoint != "http://minio:9000" {
		t.Fatalf("recording %+v", c.Recording)
	}
}
