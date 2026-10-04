package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnvFileDoesNotExecuteShellAndHonorsEnvironment(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("OPENAI_BASE_URL", "https://api.openai.com/v1")
	p := filepath.Join(t.TempDir(), "env")
	os.WriteFile(p, []byte("export OPENAI_API_KEY='$(do-not-execute)'\nOPENAI_BASE_URL=ignored\nOTHER=ignored\n"), 0600)
	if err := loadEnv(p); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("OPENAI_API_KEY") != "$(do-not-execute)" || os.Getenv("OPENAI_BASE_URL") != "https://api.openai.com/v1" {
		t.Fatal("bad dotenv behavior")
	}
}
