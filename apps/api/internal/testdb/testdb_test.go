package testdb_test

import (
	"strings"
	"testing"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
	"github.com/keeptrue/sforum/apps/api/internal/testdb/guard"
)

const (
	disposableURL  = "postgres://tester:tester@127.0.0.1:55432/sforum_test_suite?sslmode=disable"
	developmentURL = "postgres://sforum:sforum@127.0.0.1:15432/sforum?sslmode=disable"
)

func TestDecideSkipsWithoutTestDatabaseURL(t *testing.T) {
	decision := testdb.Decide("", developmentURL, guard.State{}, nil)
	if decision.Outcome != testdb.Skip {
		t.Fatalf("outcome = %v, want Skip", decision.Outcome)
	}
	if !strings.Contains(decision.Reason, "test-api-isolated.sh") {
		t.Fatalf("skip reason %q must point at the isolated runner", decision.Reason)
	}
}

func TestDecideRefusesWhenProbeFails(t *testing.T) {
	decision := testdb.Decide(disposableURL, developmentURL, guard.State{}, errProbe)
	if decision.Outcome != testdb.Refuse {
		t.Fatalf("outcome = %v, want Refuse", decision.Outcome)
	}
	if strings.Contains(decision.Reason, "tester:tester") {
		t.Fatalf("refusal %q must redact the password", decision.Reason)
	}
}

func TestDecideRefusesSentinelDatabaseEvenOnDisposableCluster(t *testing.T) {
	decision := testdb.Decide(disposableURL, developmentURL, guard.State{
		Sentinel:   true,
		Disposable: true,
	}, nil)
	if decision.Outcome != testdb.Refuse {
		t.Fatalf("outcome = %v, want Refuse", decision.Outcome)
	}
	if !strings.Contains(decision.Reason, guard.SentinelTable) {
		t.Fatalf("reason %q must name the sentinel table", decision.Reason)
	}
}

func TestDecideAllowsDisposableClusterSharingTheDevelopmentURL(t *testing.T) {
	// scripts/test-api-isolated.sh 把两个变量都指向同一个一次性集群。
	decision := testdb.Decide(disposableURL, disposableURL, guard.State{Disposable: true}, nil)
	if decision.Outcome != testdb.Allow {
		t.Fatalf("outcome = %v, want Allow", decision.Outcome)
	}
}

func TestDecideRefusesInheritedDevelopmentURL(t *testing.T) {
	decision := testdb.Decide(developmentURL, developmentURL, guard.State{}, nil)
	if decision.Outcome != testdb.Refuse {
		t.Fatalf("outcome = %v, want Refuse", decision.Outcome)
	}
	if !strings.Contains(decision.Reason, testdb.EnvDevelopmentDatabaseURL) {
		t.Fatalf("reason %q must name the inherited variable", decision.Reason)
	}
}

func TestDecideRefusesClusterHostingDevelopmentDatabase(t *testing.T) {
	decision := testdb.Decide(disposableURL, "", guard.State{DevelopmentDatabase: true}, nil)
	if decision.Outcome != testdb.Refuse {
		t.Fatalf("outcome = %v, want Refuse", decision.Outcome)
	}
	if !strings.Contains(decision.Reason, guard.DevelopmentDatabaseName) {
		t.Fatalf("reason %q must name the development database", decision.Reason)
	}
}

func TestDecideAllowsExplicitIsolatedDatabase(t *testing.T) {
	decision := testdb.Decide(disposableURL, developmentURL, guard.State{}, nil)
	if decision.Outcome != testdb.Allow {
		t.Fatalf("outcome = %v, want Allow", decision.Outcome)
	}
}

func TestIsolatedURLSkipsWithoutTestDatabaseURL(t *testing.T) {
	t.Setenv(testdb.EnvTestDatabaseURL, "")
	t.Setenv(testdb.EnvDevelopmentDatabaseURL, developmentURL)
	// t.Run 对"跳过"和"通过"都返回 true，只能在子测试收尾时读 t.Skipped()。
	var skipped bool
	t.Run("inner", func(t *testing.T) {
		t.Cleanup(func() { skipped = t.Skipped() })
		testdb.IsolatedURL(t)
	})
	if !skipped {
		t.Fatal("IsolatedURL must skip when the test database URL is absent, never inherit DATABASE_URL")
	}
}

type probeFailure struct{}

func (probeFailure) Error() string { return "connect failed" }

var errProbe = probeFailure{}
